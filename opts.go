package header

type HeadOption func(opts *HeadRequestParams)

// HeadRequestParams contains options to be used for header Exchange
// requests.
type HeadRequestParams struct {
	// SubjectiveInit determines whether the Exchange should use
	// trusted peers for its Head request (true = yes).
	SubjectiveInit bool
}

func DefaultHeadRequestParams() HeadRequestParams {
	return HeadRequestParams{
		SubjectiveInit: true,
	}
}

// WithDisabledSubjectiveInit sets the SubjectiveInit parameter to false,
// indicating to the Head method to use the tracked peerset rather than
// the trusted peerset (if enough tracked peers are available)
func WithDisabledSubjectiveInit(opts *HeadRequestParams) {
	opts.SubjectiveInit = false
}
