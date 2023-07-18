package header

type HeadOption func(opts *HeadParams)

// HeadParams contains options to be used for Head interface methods
type HeadParams struct {
	// TrustedHead allows the caller of Head to specify a trusted header
	// against which the underlying implementation of Head can verify against.
	TrustedHead Header
}

// WithTrustedHead sets the TrustedHead parameter to the given header.
func WithTrustedHead(verified Header) func(opts *HeadParams) {
	return func(opts *HeadParams) {
		opts.TrustedHead = verified
	}
}
