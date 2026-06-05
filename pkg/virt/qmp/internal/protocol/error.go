package protocol

// ResponseError reports a typed QMP error response.
type ResponseError struct {
	Class       string
	Description string
}

func (e *ResponseError) Error() string {
	if e.Class == "" {
		return e.Description
	}
	return e.Class + ": " + e.Description
}
