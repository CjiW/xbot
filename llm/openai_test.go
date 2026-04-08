package llm

import (
	"errors"
	"testing"
)

func TestIsMaxTokensParamError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "max_tokens rejected, suggests max_completion_tokens",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","code":"invalid_request_body"}`),
			want: "use_new",
		},
		{
			name: "max_completion_tokens rejected, no suggestion",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_completion_tokens' is not supported with this model.","code":"invalid_request_body"}`),
			want: "use_legacy",
		},
		{
			name: "max_completion_tokens rejected, suggests max_tokens",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_completion_tokens' is not supported with this model. Use 'max_tokens' instead.","code":"invalid_request_body"}`),
			want: "use_legacy",
		},
		{
			name: "unrelated 400 error",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"invalid model","code":"invalid_request_body"}`),
			want: "",
		},
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "max_tokens mentioned but not unsupported",
			err:  errors.New(`max_tokens must be a positive integer`),
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isMaxTokensParamError(c.err)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
