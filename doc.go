// Package mineskin is a client for the MineSkin V2 API (https://api.mineskin.org).
//
// It depends only on the Go standard library. A Client is safe for concurrent
// use by multiple goroutines once it has been created.
//
// Errors returned by the package will be exposed as sentinel values
// (ErrClient, ErrInvalidInput, ErrNotFound) that can be matched with
// errors.Is; they are not defined yet.
package mineskin
