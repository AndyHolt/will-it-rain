package httpx

import "fmt"

// errorBodyLimit is how much of a failed response body to quote back. Even when
// calling APIs which answer with a short JSON reason, a proxy or auth failure
// in front of the API can return an arbitrarily large HTML page.
const errorBodyLimit = 2048

// StatusError is a response other than 200 OK, carrying enough of the body to
// tell an auth failure from a missing resource without turning on request
// logging.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body)
}
