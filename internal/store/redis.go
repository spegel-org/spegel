package store

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/rueian/rueidis"
	"go.uber.org/multierr"

	"github.com/xenitab/spegel/internal/discover"
)

type RedisStore struct {
	podIP  string
	d      discover.Discover
	client rueidis.Client
}

func NewRedisStore(podIP string, d discover.Discover, redisAddr string) (Store, error) {
	opts := rueidis.ClientOption{
		DisableCache: true,
		InitAddress:  []string{redisAddr},
	}
	client, err := rueidis.NewClient(opts)
	if err != nil {
		return nil, err
	}
	return &RedisStore{
		podIP:  podIP,
		d:      d,
		client: client,
	}, nil
}

func (r *RedisStore) Start() error {
	return nil
}

func (r *RedisStore) Ready(ctx context.Context) error {
	return nil
}

func (r *RedisStore) Stop() error {
	return nil
}

func (r *RedisStore) Add(ctx context.Context, layers []string) error {
	expirationSeconds := int64(KeyExpiration.Seconds())
	errs := []error{}
	for _, layer := range layers {
		key := getKey(r.podIP, layer)
		err := r.client.Do(ctx, r.client.B().Set().Key(key).Value(r.podIP).ExSeconds(expirationSeconds).Build()).Error()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return multierr.Combine(errs...)
}
func (r *RedisStore) Remove(ctx context.Context, layers []string) error {
	errs := []error{}
	for _, layer := range layers {
		key := getKey(r.podIP, layer)
		err := r.client.Do(ctx, r.client.B().Del().Key(key).Build()).Error()
		if err != nil {
			errs = append(errs)
		}
	}
	return multierr.Combine(errs...)
}
func (r *RedisStore) Get(ctx context.Context, layer string) ([]string, error) {
	peers, err := r.d.GetPeers(ctx)
	if err != nil {
		return nil, err
	}
	logr.FromContextOrDiscard(ctx).Info("looking for layers", "peers", peers)
	ips := []string{}
	for _, peer := range peers {
		// Skip self when lookip at peers
		if peer == r.podIP {
			continue
		}
		key := getKey(peer, layer)
		ip, err := r.client.Do(ctx, r.client.B().Get().Key(key).Build()).ToString()
		if err != nil && rueidis.IsRedisNil(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func (r *RedisStore) ResetExpiration(ctx context.Context, layers []string) error {
	expirationSeconds := int64(KeyExpiration.Seconds())
	errs := []error{}
	for _, layer := range layers {
		key := getKey(r.podIP, layer)
		err := r.client.Do(ctx, r.client.B().Expire().Key(key).Seconds(expirationSeconds).Build()).Error()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return multierr.Combine(errs...)
}

func (r *RedisStore) Dump(ctx context.Context) ([]string, error) {
	data := []string{}
	var scan rueidis.ScanEntry
	for more := true; more; more = scan.Cursor != 0 {
		scan, err := r.client.Do(ctx, r.client.B().Scan().Cursor(scan.Cursor).Match("layer:*").Build()).AsScanEntry()
		if err != nil {
			return nil, err
		}
		data = append(data, scan.Elements...)
	}
	return data, nil
}
