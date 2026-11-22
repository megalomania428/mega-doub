// cspell:ignore awserr duplicacy econnrefused econnreset gilbertchen goamz
// cspell:ignore googleapi hubic ncw

package dup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	duplicacy "duplicacy.invalid/v3/src"
	"github.com/aws/aws-sdk-go/aws/awserr"
	azure "github.com/gilbertchen/azure-sdk-for-go/storage"
	dropbox "github.com/gilbertchen/go-dropbox"
	"github.com/gilbertchen/goamz/s3"
	"github.com/ncw/swift/v2"
	"google.golang.org/api/googleapi"
)

func Test_transient(t *testing.T) {
	refused := &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
	exception := &Error{Op: opCreateStorage, LogID: "STORAGE_CREATE", Err: refused}
	reset := &url.Error{Op: "Get", URL: "https://a.invalid/", Err: syscall.ECONNRESET}
	slowDown := awserr.NewRequestFailure(awserr.New("SlowDown", "slow", nil), 503, "")
	forbidden := awserr.NewRequestFailure(awserr.New("Forbidden", "no", nil), 403, "")
	// nested wraps reset in depth errors of aws-sdk-go.
	nested := func(depth int) error {
		err := error(reset)
		for range depth {
			err = awserr.New("RequestError", "send request failed", err)
		}
		return err
	}
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{name: "nil", args: args{err: nil}, want: false},
		{name: "refused", args: args{err: refused}, want: true},
		{name: "refused in an Exception", args: args{err: exception}, want: true},
		{name: "reset", args: args{err: reset}, want: true},
		{name: "unexpected EOF", args: args{err: io.ErrUnexpectedEOF}, want: true},
		{name: "EOF", args: args{err: io.EOF}, want: false},
		{name: "deadline", args: args{err: context.DeadlineExceeded}, want: true},
		{name: "canceled", args: args{err: context.Canceled}, want: false},
		{
			name: "DNS timeout",
			args: args{err: &net.DNSError{Err: "timeout", IsTimeout: true}},
			want: true,
		},
		{
			name: "DNS temporary",
			args: args{err: &net.DNSError{Err: "busy", IsTemporary: true}},
			want: true,
		},
		{
			name: "DNS not found",
			args: args{err: &net.DNSError{Err: "no host", IsNotFound: true}},
			want: false,
		},
		{
			name: "not a directory",
			args: args{err: &os.PathError{Op: "stat", Path: "/a", Err: syscall.ENOTDIR}},
			want: false,
		},
		{name: "AWS 503", args: args{err: slowDown}, want: true},
		{name: "AWS 403", args: args{err: forbidden}, want: false},
		{
			name: "AWS network at the last step",
			args: args{err: nested(maxOrigErr)},
			want: true,
		},
		{
			name: "AWS network too deep",
			args: args{err: nested(maxOrigErr + 1)},
			want: false,
		},
		{name: "google 600", args: args{err: &googleapi.Error{Code: 600}}, want: false},
		{
			name: "AWS network",
			args: args{err: awserr.New("RequestError", "send request failed", reset)},
			want: true,
		},
		{name: "goamz 500", args: args{err: &s3.Error{StatusCode: 500}}, want: true},
		{name: "goamz 404", args: args{err: &s3.Error{StatusCode: 404}}, want: false},
		{
			name: "dropbox 502",
			args: args{err: &dropbox.Error{StatusCode: 502}},
			want: true,
		},
		{
			name: "azure 503",
			args: args{err: azure.AzureStorageServiceError{StatusCode: 503}},
			want: true,
		},
		{
			name: "azure 409",
			args: args{err: azure.AzureStorageServiceError{StatusCode: 409}},
			want: false,
		},
		{name: "google 429", args: args{err: &googleapi.Error{Code: 429}}, want: true},
		{name: "google 400", args: args{err: &googleapi.Error{Code: 400}}, want: false},
		{
			name: "swift 503 wrapped",
			args: args{err: fmt.Errorf("list: %w", &swift.Error{StatusCode: 503})},
			want: true,
		},
		{name: "ACD 503", args: args{err: duplicacy.ACDError{Status: 503}}, want: true},
		{
			name: "ACD pointer 500",
			args: args{err: &duplicacy.ACDError{Status: 500}},
			want: true,
		},
		{
			name: "hubic 429",
			args: args{err: duplicacy.HubicError{Status: 429}},
			want: true,
		},
		{
			name: "hubic 404",
			args: args{err: duplicacy.HubicError{Status: 404}},
			want: false,
		},
		{
			name: "text only",
			args: args{err: errors.New("maximum backoff reached")},
			want: false,
		},
		{name: "wrong password", args: args{err: errWrongPassword}, want: false},
		{name: "not initialized", args: args{err: ErrNotInitialized}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transient(tt.args.err); got != tt.want {
				t.Errorf("transient() = %v, want %v", got, tt.want)
			}
		})
	}
}
