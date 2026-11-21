// cspell:ignore awserr duplicacy econnrefused econnreset gilbertchen goamz
// cspell:ignore googleapi hubic ncw

// Package dup is documented in log.go.
package dup

import (
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	duplicacy "duplicacy.invalid/v3/src"
	"github.com/aws/aws-sdk-go/aws/awserr"
	azure "github.com/gilbertchen/azure-sdk-for-go/storage"
	dropbox "github.com/gilbertchen/go-dropbox"
	"github.com/gilbertchen/goamz/s3"
	"github.com/ncw/swift/v2"
	"google.golang.org/api/googleapi"
)

// maxOrigErr bounds the number of the steps of the walk over the original errors of
// aws-sdk-go.
const maxOrigErr = 8

// statusCodes take the HTTP status out of the error types of the SDKs of the backends
// of duplicacy p003 that reach the caller through Unwrap.
var statusCodes = []func(error) (int, bool){
	statusOf(func(e awserr.RequestFailure) int { return e.StatusCode() }),
	statusOf(func(e *s3.Error) int { return e.StatusCode }),
	statusOf(func(e *dropbox.Error) int { return e.StatusCode }),
	statusOf(func(e azure.AzureStorageServiceError) int { return e.StatusCode }),
	statusOf(func(e azure.UnexpectedStatusCodeError) int { return e.Got() }),
	statusOf(func(e *googleapi.Error) int { return e.Code }),
	statusOf(func(e *swift.Error) int { return e.StatusCode }),
	statusOf(func(e duplicacy.ACDError) int { return e.Status }),
	statusOf(func(e *duplicacy.ACDError) int { return e.Status }),
	statusOf(func(e duplicacy.HubicError) int { return e.Status }),
}

// statusOf returns a function that takes the HTTP status out of the first error of the
// type E in the chain of an error with code.
func statusOf[E error](code func(E) int) func(error) (int, bool) {
	return func(err error) (int, bool) {
		e, ok := errors.AsType[E](err)
		if !ok {
			return 0, false
		}
		return code(e), true
	}
}

// transient reports whether err, a failure of a call to the storage, is temporary, so
// that the attempt is worth a retry. The type of the cause decides, found through Unwrap
// and, for aws-sdk-go, which has no Unwrap, through OrigErr: a timeout of net.Error, a
// temporary or timed out DNS lookup, a refused or reset connection, a connection broken
// in the middle of a response (io.ErrUnexpectedEOF), and the HTTP statuses 429 and 5xx of
// the error types of statusCodes, Hubic and ACD included; the errStaleListing of the read
// of a new revision is temporary too. Everything else is permanent, the text of an error
// is never matched: the failures that reach the caller as text only, such as those of
// WebDAV, the B2 statuses, File Fabric and the SMB transport, fail at once, and so do the
// statuses of OneDrive, whose type is not in statusCodes. A failure that Storj, S3 or
// Azure report as a missing file is not an error: Open takes it for a missing config,
// which is errNotInitialized without WithInit and the start of the initialization with
// it.
func transient(err error) bool {
	if errors.Is(err, errStaleListing) {
		return true
	}
	for range maxOrigErr + 1 {
		if err == nil {
			return false
		}
		if transientNet(err) || transientStatus(err) {
			return true
		}
		e, ok := errors.AsType[awserr.Error](err)
		if !ok {
			return false
		}
		err = e.OrigErr()
	}
	return false
}

// transientNet reports whether err is a temporary failure of the network.
func transientNet(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if e, ok := errors.AsType[*net.DNSError](err); ok {
		return e.IsTimeout || e.IsTemporary
	}
	e, ok := errors.AsType[net.Error](err)
	return ok && e.Timeout()
}

// transientStatus reports whether err holds the HTTP status 429 or 5xx in one of the
// error types of statusCodes.
func transientStatus(err error) bool {
	for _, status := range statusCodes {
		code, ok := status(err)
		if ok && (code == http.StatusTooManyRequests ||
			code >= http.StatusInternalServerError && code < 600) {
			return true
		}
	}
	return false
}
