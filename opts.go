package header

type HeadOption func(opts *HeadParams)

// HeadParams contains options to be used for header Exchange
// requests.
type HeadParams struct {
	// DisableSubjectiveInit indicates whether the Exchange should use
	// trusted peers for its Head request. If nil, trusted peers will
	// be used. If non-nil, Exchange will ask several peers from its
	// network for their Head and verify against the given header.
	DisableSubjectiveInit Header
}

func DefaultHeadParams() HeadParams {
	return HeadParams{
		DisableSubjectiveInit: nil,
	}
}

// WithDisabledSubjectiveInit sets the DisableSubjectiveInit parameter to the
// given header, indicating to the Head method to use the tracked peerset rather
// than the trusted peerset (if enough tracked peers are available).
func WithDisabledSubjectiveInit(verified Header) func(opts *HeadParams) {
	return func(opts *HeadParams) {
		opts.DisableSubjectiveInit = verified
	}
}
