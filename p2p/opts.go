package p2p

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

// WithSubjectiveInit TODO
func WithSubjectiveInit(opts *RequestParams) {
	opts.SubjectiveInit = true
}
