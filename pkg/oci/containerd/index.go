package containerd

import (
	"context"
	"errors"
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

var (
	MissingDigestErr = errors.New("image needs digest to be indexed")
)

type WalkFunc func(ctx context.Context, img oci.Image) ([]digest.Digest, error)

type Index struct {
	imageTagIdx     map[string]oci.Image
	imageContentIdx map[oci.Image]map[digest.Digest]any
	contentRefCount map[digest.Digest]int
	walkFn          WalkFunc
}

func NewIndex(walkFn WalkFunc) *Index {
	return &Index{
		imageTagIdx:     map[string]oci.Image{},
		imageContentIdx: map[oci.Image]map[digest.Digest]any{},
		contentRefCount: map[digest.Digest]int{},
		walkFn:          walkFn,
	}
}

func (idx *Index) Walk(ctx context.Context, img oci.Image) ([]store.Event, error) {
	if img.Digest == "" {
		return nil, MissingDigestErr
	}

	events := []store.Event{}

	tagName, tagNameOk := img.TagName()
	dgsts, err := idx.walkFn(ctx, img)
	if err != nil {
		return nil, err
	}
	oldDgsts := []digest.Digest{}
	if tagNameOk {
		oldImg, oldImgOk := idx.imageTagIdx[tagName]
		if oldImgOk {
			oldImg.Digest = ""
			oldDgsts, err = idx.walkFn(ctx, oldImg)
			if err != nil && errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
	}

	imgContent, ok := idx.imageContentIdx[img]
	if !ok {
		imgContent = map[digest.Digest]any{}
	}
	for _, dgst := range dgsts {
		if _, ok := imgContent[dgst]; ok {
			continue
		}
		imgContent[dgst] = nil
		count := idx.contentRefCount[dgst]
		count += 1
		if count == 1 {
			event := store.Event{
				Type:   store.CreateEvent,
				Digest: dgst,
			}
			events = append(events, event)
		}
		idx.contentRefCount[dgst] = count
	}
	idx.imageContentIdx[img] = imgContent
	// if tagNameOk {
	// 	events[0].Reference = tagName
	// }

	for _, dgst := range oldDgsts {
		count := idx.contentRefCount[dgst]
		count -= 1
		if count == 0 {
			event := store.Event{
				Type:   store.CreateEvent,
				Digest: dgst,
			}
			events = append(events, event)
			delete(idx.contentRefCount, dgst)
		} else {
			idx.contentRefCount[dgst] = count
		}
	}
	// idx.imageTagIdx[tagName] = img

	return events, nil
}

func (idx *Index) Remove(ctx context.Context, img oci.Image) ([]store.Event, error) {
	if img.Digest == "" {
		return nil, MissingDigestErr
	}

	fmt.Println("removing", img)

	events := []store.Event{}

	tagName, tagNameOk := img.TagName()
	imgContent, ok := idx.imageContentIdx[img]
	if !ok {
		return nil, nil
	}
	delete(idx.imageContentIdx, img)
	for dgst := range imgContent {
		count := idx.contentRefCount[dgst]
		count -= 1
		if count == 0 {
			event := store.Event{
				Type:   store.CreateEvent,
				Digest: dgst,
			}
			events = append(events, event)
			delete(idx.contentRefCount, dgst)
		}
	}
	if tagNameOk {
		// events[0].Reference = tagName
		delete(idx.imageTagIdx, tagName)
	}

	return events, nil
}
