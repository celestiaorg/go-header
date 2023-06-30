package header

type RequestOption func(opts *RequestParams)

// RequestParams contains options to be used for header Exchange
// requests.
type RequestParams struct {
	// SubjectiveInit determines whether the Exchange should use
	// trusted peers for its Head request (true = yes).
	SubjectiveInit bool
}

func DefaultRequestParams() RequestParams {
	return RequestParams{
		SubjectiveInit: false,
	}
}

// WithSubjectiveInit sets the SubjectiveInit parameter to true,
// indicating to the Head method to use the trusted peer set.
func WithSubjectiveInit(opts *RequestParams) {
	opts.SubjectiveInit = true
}
