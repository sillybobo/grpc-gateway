package runtime

import (
	"context"
	"io"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/internal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/status"
)

// HTTPStatusFromCode converts a gRPC error code into the corresponding HTTP response status.
// See: https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto
func HTTPStatusFromCode(code codes.Code) int {
	switch code {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return http.StatusRequestTimeout
	case codes.Unknown:
		return http.StatusInternalServerError
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.FailedPrecondition:
		// Note, this deliberately doesn't translate to the similarly named '412 Precondition Failed' HTTP response status.
		return http.StatusBadRequest
	case codes.Aborted:
		return http.StatusConflict
	case codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Internal:
		return http.StatusInternalServerError
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DataLoss:
		return http.StatusInternalServerError
	}

	grpclog.Infof("Unknown gRPC error code: %v", code)
	return http.StatusInternalServerError
}

var (
	// HTTPError replies to the request with an error.
	//
	// HTTPError is called:
	//  - From generated per-endpoint gateway handler code, when calling the backend results in an error.
	//  - From gateway runtime code, when forwarding the response message results in an error.
	//
	// The default value for HTTPError calls the custom error handler configured on the ServeMux via the
	// WithProtoErrorHandler serve option if that option was used, calling GlobalHTTPErrorHandler otherwise.
	//
	// To customize the error handling of a particular ServeMux instance, use the WithProtoErrorHandler
	// serve option.
	//
	// To customize the error format for all ServeMux instances not using the WithProtoErrorHandler serve
	// option, set GlobalHTTPErrorHandler to a custom function.
	//
	// Setting this variable directly to customize error format is deprecated.
	HTTPError = MuxOrGlobalHTTPError

	// GlobalHTTPErrorHandler is the HTTPError handler for all ServeMux instances not using the
	// WithProtoErrorHandler serve option.
	//
	// You can set a custom function to this variable to customize error format.
	GlobalHTTPErrorHandler = DefaultHTTPError

	// OtherErrorHandler handles gateway errors from parsing and routing client requests for all
	// ServeMux instances not using the WithProtoErrorHandler serve option.
	//
	// It returns the following error codes: StatusMethodNotAllowed StatusNotFound StatusBadRequest
	//
	// To customize parsing and routing error handling of a particular ServeMux instance, use the
	// WithProtoErrorHandler serve option.
	//
	// To customize parsing and routing error handling of all ServeMux instances not using the
	// WithProtoErrorHandler serve option, set a custom function to this variable.
	OtherErrorHandler = DefaultOtherErrorHandler
)

// MuxOrGlobalHTTPError uses the mux-configured error handler, falling back to GlobalErrorHandler.
func MuxOrGlobalHTTPError(ctx context.Context, mux *ServeMux, marshaler Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	if mux.protoErrorHandler != nil {
		mux.protoErrorHandler(ctx, mux, marshaler, w, r, err)
	} else {
		GlobalHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
	}
}

// DefaultHTTPError is the default implementation of HTTPError.
// If "err" is an error from gRPC system, the function replies with the status code mapped by HTTPStatusFromCode.
// If otherwise, it replies with http.StatusInternalServerError.
//
// The response body returned by this function is a JSON object,
// which contains a member whose key is "error" and whose value is err.Error().
func DefaultHTTPError(ctx context.Context, mux *ServeMux, marshaler Marshaler, w http.ResponseWriter, _ *http.Request, err error) {
	const fallback = `{"error": "failed to marshal error message"}`

	s, ok := status.FromError(err)
	if !ok {
		s = status.New(codes.Unknown, err.Error())
	}

	w.Header().Del("Trailer")

	contentType := marshaler.ContentType()
	// Check marshaler on run time in order to keep backwards compatability
	// An interface param needs to be added to the ContentType() function on
	// the Marshal interface to be able to remove this check
	if httpBodyMarshaler, ok := marshaler.(*HTTPBodyMarshaler); ok {
		pb := s.Proto()
		contentType = httpBodyMarshaler.ContentTypeFromMessage(pb)
	}
	w.Header().Set("Content-Type", contentType)

	body := &internal.Error{
		Error:   s.Message(),
		Message: s.Message(),
		Code:    int32(s.Code()),
		Details: s.Proto().GetDetails(),
	}

	buf, merr := marshaler.Marshal(body)
	if merr != nil {
		grpclog.Infof("Failed to marshal error message %q: %v", body, merr)
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := io.WriteString(w, fallback); err != nil {
			grpclog.Infof("Failed to write response: %v", err)
		}
		return
	}

	md, ok := ServerMetadataFromContext(ctx)
	if !ok {
		grpclog.Infof("Failed to extract ServerMetadata from context")
	}

	handleForwardResponseServerMetadata(w, mux, md)
	handleForwardResponseTrailerHeader(w, md)
	st := HTTPStatusFromCode(s.Code())
	w.WriteHeader(st)
	if _, err := w.Write(buf); err != nil {
		grpclog.Infof("Failed to write response: %v", err)
	}

	handleForwardResponseTrailer(w, md)
}

// DefaultOtherErrorHandler is the default implementation of OtherErrorHandler.
// It simply writes a string representation of the given error into "w".
func DefaultOtherErrorHandler(w http.ResponseWriter, _ *http.Request, msg string, code int) {
	http.Error(w, msg, code)
}
