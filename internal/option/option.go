package option

type Option[T any] func(*T) error

func Apply[T any](cfg *T, opts ...Option[T]) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		err := opt(cfg)
		if err != nil {
			return err
		}
	}
	return nil
}
