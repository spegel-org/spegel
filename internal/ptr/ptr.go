package ptr

// To converts the given value to a pointer to the value.
func To[T any](v T) *T {
	return &v
}
