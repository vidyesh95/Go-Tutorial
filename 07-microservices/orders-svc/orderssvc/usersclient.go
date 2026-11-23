package orderssvc

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
)

// User is the slice of a user that orders-svc cares about. Owning a tiny local type (instead of
// passing the protobuf type around) means a change in users.proto touches one file here.
type User struct {
	ID    string
	Name  string
	Email string
}

// Errors the UsersClient returns. The HTTP layer maps them to statuses (api.go).
var (
	ErrUserNotFound     = errors.New("unknown user")
	ErrUsersUnavailable = errors.New("users service unavailable")
)

// UsersClient is what orders-svc needs from users-svc. Tests substitute a fake; production uses
// GRPCUsers. Declaring the interface in the CONSUMER, sized to what it uses, is idiomatic Go.
type UsersClient interface {
	GetUser(ctx context.Context, id string) (User, error)
}

// GRPCUsers calls users-svc over gRPC with a per-call deadline and retries.
type GRPCUsers struct {
	Client      usersv1.UsersServiceClient
	Timeout     time.Duration // deadline for EACH attempt
	MaxAttempts int           // total tries, including the first
	BaseBackoff time.Duration // first wait; doubles each retry
}

// GetUser fetches a user, translating gRPC status codes into this package's errors.
//
// TIMEOUTS / DEADLINES: every attempt gets its own deadline via context.WithTimeout. gRPC sends
// it on the wire (the grpc-timeout header), so users-svc sees the same deadline in ITS context
// and can stop work the caller has already given up on. Without a deadline, one slow dependency
// makes every caller's goroutines pile up until the whole system falls over.
// The attempt context derives from ctx, so if the original client cancels or the gateway's
// deadline passes, the call is abandoned immediately: deadlines propagate down the chain.
//
// RETRIES: only codes.Unavailable (connection refused, server restarting, load balancer
// switching backends) is retried; it means "the request most likely never ran". We do NOT retry:
//   - NotFound / InvalidArgument: retrying cannot change the answer;
//   - DeadlineExceeded: the time budget is already spent, and retrying while a dependency is
//     struggling multiplies its load (a "retry storm" turns a blip into an outage).
//
// Retrying is only safe because GetUser is a read. Retrying a write needs an idempotency key
// (which is exactly why POST /orders requires one).
//
// BACKOFF waits 1x, 2x, 4x... the base with jitter, so that many clients do not all retry in
// lockstep and hammer a recovering service at the same instant.
func (g *GRPCUsers) GetUser(ctx context.Context, id string) (User, error) {
	attempts := max(g.MaxAttempts, 1)
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			if err := sleep(ctx, g.backoff(attempt)); err != nil {
				return User{}, fmt.Errorf("%w: %w", ErrUsersUnavailable, err)
			}
		}
		resp, err := g.attempt(ctx, id)
		if err == nil {
			u := resp.GetUser()
			return User{ID: u.GetId(), Name: u.GetName(), Email: u.GetEmail()}, nil
		}
		switch status.Code(err) {
		case codes.NotFound:
			return User{}, ErrUserNotFound
		case codes.Unavailable:
			lastErr = err
			continue // retry
		case codes.DeadlineExceeded, codes.Canceled:
			return User{}, fmt.Errorf("%w: %w", ErrUsersUnavailable, err)
		default:
			return User{}, fmt.Errorf("users-svc GetUser: %w", err)
		}
	}
	return User{}, fmt.Errorf("%w after %d attempts: %w", ErrUsersUnavailable, attempts, lastErr)
}

func (g *GRPCUsers) attempt(ctx context.Context, id string) (*usersv1.GetUserResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel() // always release the timer, even on success
	return g.Client.GetUser(ctx, &usersv1.GetUserRequest{Id: id})
}

// backoff returns the wait before retry number n (n=1 is the first retry): exponential growth
// with "equal jitter" (half fixed, half random).
func (g *GRPCUsers) backoff(n int) time.Duration {
	d := g.BaseBackoff << (n - 1)
	if d < 2 {
		return d
	}
	return d/2 + rand.N(d/2)
}

// sleep waits for d or until ctx is done, whichever comes first (time.Sleep ignores ctx).
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
