package header

type HeadOption func(opts *HeadRequestParams)

// HeadRequestParams contains options to be used for header Exchange
// requests.
type HeadRequestParams struct {
	// DisableSubjectiveInit indicates whether the Exchange should use
	// trusted peers for its Head request. If nil, trusted peers will
	// be used. If non-nil, Exchange will ask several peers from its
	// network for their Head and verify against the given header.
	DisableSubjectiveInit Header
}

func DefaultHeadRequestParams() HeadRequestParams {
	return HeadRequestParams{
		DisableSubjectiveInit: nil,
	}
}

// WithDisabledSubjectiveInit sets the DisableSubjectiveInit parameter to the
// given header, indicating to the Head method to use the tracked peerset rather
// than the trusted peerset (if enough tracked peers are available).
func WithDisabledSubjectiveInit(verified Header) func(opts *HeadRequestParams) {
	return func(opts *HeadRequestParams) {
		opts.DisableSubjectiveInit = verified
	}
}
