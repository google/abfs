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

package instance

import (
	"errors"
	"fmt"
)

// We only support GCP user or service account authentication.
// This can move to a plugin based solution in the future if required
// to enable users to extend.
type AuthType string
type AuthInfo struct {
	Type       AuthType
	Audience   string
	RootCAFile string
	DisableTLS bool
}

const (
	AuthTypeNone    AuthType = "none"
	AuthTypeGCPUser AuthType = "gcpuser"
	AuthTypeGCPSA   AuthType = "gcpsa"
)

func (t *AuthType) Set(value string) error {
	switch AuthType(value) {
	case AuthTypeNone:
		*t = AuthTypeNone
	case AuthTypeGCPUser:
		*t = AuthTypeGCPUser
	case AuthTypeGCPSA:
		*t = AuthTypeGCPSA
	default:
		return errors.New("invalid auth type")
	}
	return nil
}
func (t *AuthType) String() string {
	return string(*t)
}

func (t *AuthType) Type() string {
	return "AuthType"
}

func AuthTypes() string {
	return fmt.Sprintf("%s,%s,%s", AuthTypeNone, AuthTypeGCPUser, AuthTypeGCPSA)
}

func (AuthType) EnumValues() [][2]string {
	return [][2]string{
		{string(AuthTypeNone), "No authentication"},
		{string(AuthTypeGCPUser), "GCP user authentication (gcloud)"},
		{string(AuthTypeGCPSA), "GCP service account authentication"},
	}
}

func CreateAuthInfo(authType AuthType, authAudience string, rootCAFile string, disableTLS bool) AuthInfo {
	return AuthInfo{
		Type:       authType,
		Audience:   authAudience,
		RootCAFile: rootCAFile,
		DisableTLS: disableTLS,
	}
}
