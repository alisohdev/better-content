package transport

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

const maxJSONBodyBytes = 1 << 20

const (
	UNAUTHORIZED           = ErrorResponseCodeUNAUTHORIZED
	FORBIDDEN              = ErrorResponseCodeFORBIDDEN
	INVALIDORIGIN          = ErrorResponseCodeINVALIDORIGIN
	CONFLICT               = ErrorResponseCodeCONFLICT
	SERVICEUNAVAILABLE     = ErrorResponseCodeSERVICEUNAVAILABLE
	RATELIMITED            = ErrorResponseCodeRATELIMITED
	INTERNALERROR          = ErrorResponseCodeINTERNALERROR
	NOTFOUND               = ErrorResponseCodeNOTFOUND
	VALIDATIONERROR        = ErrorResponseCodeVALIDATIONERROR
	INVALIDNAME            = ErrorResponseCodeINVALIDNAME
	INVALIDEMAILORPASSWORD = ErrorResponseCodeINVALIDEMAILORPASSWORD
)

var (
	errInvalidJSON       = errors.New("invalid JSON request body")
	errUnsupportedType   = errors.New("request content type must be application/json")
	errRequestTooLarge   = errors.New("request body is too large")
	errUnexpectedContent = errors.New("request body must contain exactly one JSON value")
)

// DecodeJSON accepts one bounded JSON object, rejects unknown fields, and does
// not include request data in errors returned to callers.
func DecodeJSON(r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errUnsupportedType
	}

	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errRequestTooLarge
		}
		return errInvalidJSON
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errUnexpectedContent
	}
	return nil
}

// WriteError emits only stable contract codes. Internal errors and decoder
// details must never be copied into a response.
func WriteError(w http.ResponseWriter, status int, code ErrorResponseCode, fields map[string]string) {
	response := ErrorResponse{Code: code}
	if len(fields) > 0 {
		response.Fields = &fields
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func WriteJSON(w http.ResponseWriter, status int, response any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func HandleDecodeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnsupportedType), errors.Is(err, errRequestTooLarge),
		errors.Is(err, errInvalidJSON), errors.Is(err, errUnexpectedContent):
		WriteError(w, http.StatusBadRequest, VALIDATIONERROR, nil)
	default:
		WriteError(w, http.StatusInternalServerError, INTERNALERROR, nil)
	}
}
