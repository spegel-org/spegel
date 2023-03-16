package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/containerd/containerd/reference"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-logr/logr"
	pkggin "github.com/xenitab/pkg/gin"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/xenitab/spegel/internal/routing"
)

type Webhook struct {
	srv *http.Server
}

func NewWebhook(ctx context.Context, addr string, selfBootstrapAddr string, router routing.Router) (*Webhook, error) {
	log := logr.FromContextOrDiscard(ctx).WithName("webhook")
	webhookHandler := &WebhookHandler{
		selfBootstrapAddr: selfBootstrapAddr,
		router:            router,
	}
	cfg := pkggin.Config{
		LogConfig: pkggin.LogConfig{
			Logger:          log,
			IncludeLatency:  true,
			IncludeClientIP: true,
			IncludeKeys:     []string{"handler"},
		},
		MetricsConfig: pkggin.MetricsConfig{
			Service: "webhook",
		},
	}
	engine := pkggin.NewEngine(cfg)
	engine.POST("/v1/mutate", webhookHandler.mutateHandler)
	srv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}
	return &Webhook{
		srv: srv,
	}, nil
}

func (w *Webhook) ListenAndServe(ctx context.Context) error {
	if err := w.srv.ListenAndServeTLS("/etc/admission-webhook/tls/tls.crt", "/etc/admission-webhook/tls/tls.key"); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (w *Webhook) Shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return w.srv.Shutdown(shutdownCtx)
}

type WebhookHandler struct {
	selfBootstrapAddr string
	router            routing.Router
}

func (w *WebhookHandler) mutateHandler(ctx *gin.Context) {
	requestReview := &admissionv1.AdmissionReview{}
	ctx.MustBindWith(requestReview, binding.JSON)
	if requestReview.Request == nil {
		ctx.AbortWithStatus(http.StatusBadRequest)
		return
	}
	pod := &corev1.Pod{}
	err := json.Unmarshal(requestReview.Request.Object.Raw, pod)
	if err != nil {
		ctx.AbortWithError(http.StatusBadRequest, err)
		return
	}

	responseReview := admissionv1.AdmissionReview{
		TypeMeta: requestReview.TypeMeta,
		Response: &admissionv1.AdmissionResponse{
			UID:     requestReview.Request.UID,
			Allowed: true,
		},
	}

	if len(pod.Spec.InitContainers) != 1 {
		ctx.JSON(http.StatusOK, responseReview)
		return
	}
	c := pod.Spec.InitContainers[0]
	ref, err := reference.Parse(c.Image)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	_, ok, err := w.router.Resolve(ctx, ref, true)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	if !ok {
		ctx.JSON(http.StatusOK, responseReview)
		return
	}
	localImage := fmt.Sprintf("%s%s", w.selfBootstrapAddr, trimHostname(ref))
	patch := fmt.Sprintf(`[{ "op": "replace", "path": "/spec/initContainers/0/image", "value": "%s" }]`, localImage)
	responseReview.Response.Patch = []byte(patch)
	pt := admissionv1.PatchTypeJSONPatch
	responseReview.Response.PatchType = &pt
	ctx.JSON(http.StatusOK, responseReview)
}

func trimHostname(ref reference.Spec) string {
	return strings.TrimPrefix(ref.String(), ref.Hostname())
}