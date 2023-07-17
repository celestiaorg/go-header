package header

type HeadOption func(opts *HeadParams)

// HeadParams contains options to be used for header Exchange
// requests.
type HeadParams struct {
	// TrustedHead indicates whether the Exchange should use
	// trusted peers for its Head request. If nil, trusted peers will
	// be used. If non-nil, Exchange will ask several peers from its
	// network for their Head and verify against the given trusted header.
	TrustedHead Header
}

func DefaultHeadParams() HeadParams {
	return HeadParams{
		TrustedHead: nil,
	}
}

// WithTrustedHead sets the TrustedHead parameter to the given header,
// indicating to the Head method to use the tracked peerset rather
// than the trusted peerset (if enough tracked peers are available).
func WithTrustedHead(verified Header) func(opts *HeadParams) {
	return func(opts *HeadParams) {
		opts.TrustedHead = verified
	}
}
