// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"

	"github.com/google/abfs/shared/instance"
)

type TokenSource interface {
	Token() (*oauth2.Token, error)
}

type GoogleUserTokenSource struct {
	ts oauth2.TokenSource
}

func NewGoogleUserTokenSource() (*GoogleUserTokenSource, error) {
	tokenSource, err := google.DefaultTokenSource(context.Background())
	if err != nil {
		return nil, fmt.Errorf("unable to get a GCP User Token: %w. If this is expected, set auth-type in the config to none", err)
	}
	return &GoogleUserTokenSource{oauth2.ReuseTokenSource(nil, tokenSource)}, nil
}

// Sending an ADC token isn't supported out-of-the-box like it is with service accounts
func (g *GoogleUserTokenSource) Token() (*oauth2.Token, error) {
	token, err := g.ts.Token()
	if err != nil {
		return nil, err
	}

	idToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("token did not contain an id_token")
	}

	return &oauth2.Token{
		AccessToken: idToken,
		TokenType:   "Bearer",
		Expiry:      token.Expiry,
	}, nil
}

type GoogleSATokenSource struct {
	ts oauth2.TokenSource
}

// idtoken libraries can get the token from the metadata server.
func NewGoogleSATokenSource(audience string) (*GoogleSATokenSource, error) {
	ctx := context.Background()
	ts, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("idtoken.NewTokenSource: %w", err)
	}
	return &GoogleSATokenSource{ts: ts}, nil
}

func (g *GoogleSATokenSource) Token() (*oauth2.Token, error) {
	return g.ts.Token()
}

func GenerateTokenSource(authInfo instance.AuthInfo) (TokenSource, error) {
	switch authInfo.Type {
	case instance.AuthTypeNone:
		return nil, nil
	case instance.AuthTypeGCPUser:
		return NewGoogleUserTokenSource()
	case instance.AuthTypeGCPSA:
		return NewGoogleSATokenSource(authInfo.Audience)
	default:
		return nil, fmt.Errorf("invalid auth type: %v", authInfo.Type)
	}
}
