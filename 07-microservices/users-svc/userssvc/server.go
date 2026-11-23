// Package userssvc is the users microservice: a gRPC server that owns user data.
//
// Why gRPC between services? A typed contract (users.proto) generates both client and server
// code, calls are binary and multiplexed over one HTTP/2 connection, and deadlines, cancellation
// and metadata (trace context) are part of the protocol. Browsers and third parties can't speak
// it easily, so it suits INTERNAL service-to-service calls while the outside world gets HTTP/JSON
// through the gateway.
//
// In industry: status codes are the API. Returning codes.Internal for "user not found" forces
// every caller to string-match messages; returning NotFound lets them react programmatically
// (the orders service maps it to HTTP 422).
package userssvc

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
)

// Server implements usersv1.UsersServiceServer.
type Server struct {
	// Embedding the Unimplemented type is required by protoc-gen-go-grpc: it makes adding an
	// RPC to the .proto a compile-safe change for servers that have not implemented it yet.
	usersv1.UnimplementedUsersServiceServer
	store *Store
}

func NewServer(store *Store) *Server { return &Server{store: store} }

func (s *Server) GetUser(ctx context.Context, req *usersv1.GetUserRequest) (*usersv1.GetUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	u, err := s.store.Get(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &usersv1.GetUserResponse{User: toProto(u)}, nil
}

func (s *Server) CreateUser(ctx context.Context, req *usersv1.CreateUserRequest) (*usersv1.CreateUserResponse, error) {
	name, email := strings.TrimSpace(req.GetName()), strings.TrimSpace(req.GetEmail())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return nil, status.Error(codes.InvalidArgument, "email is not a valid address")
	}
	u, err := s.store.Create(ctx, name, email)
	if err != nil {
		return nil, toStatus(err)
	}
	return &usersv1.CreateUserResponse{User: toProto(u)}, nil
}

// toStatus is the single place domain errors become gRPC status codes.
func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrEmailTaken):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	default:
		// Never leak internal details (SQL text, file paths) to callers; they are in the logs.
		return status.Error(codes.Internal, "internal error")
	}
}

func toProto(u User) *usersv1.User {
	return &usersv1.User{
		Id: u.ID, Name: u.Name, Email: u.Email,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}
