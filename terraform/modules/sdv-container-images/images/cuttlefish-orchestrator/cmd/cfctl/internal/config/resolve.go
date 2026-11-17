// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The environment variables Resolve reads. A variable that is set and empty
// clears the field, except CFCTL_PROFILE, where empty means unset.
const (
	EnvProfile     = "CFCTL_PROFILE"
	EnvServer      = "CFCTL_SERVER"
	EnvAudience    = "CFCTL_AUDIENCE"
	EnvImpersonate = "CFCTL_IMPERSONATE"
	EnvCredentials = "CFCTL_CREDENTIALS"
	EnvAuthNone    = "CFCTL_AUTH_NONE"
	EnvProxyEmail  = "CFCTL_PROXY_EMAIL"
)

// Source names where an effective value came from.
type Source string

// The value sources, strongest first. SourceUnset means no layer set it.
const (
	SourceFlag    Source = "flag"
	SourceEnv     Source = "env"
	SourceProfile Source = "profile"
	SourceUnset   Source = "unset"
)

// Sources records the source of each effective field.
type Sources struct {
	Server                    Source
	CredentialsPath           Source
	IAPAudience               Source
	AuthNone                  Source
	ProxyEmail                Source
	ImpersonateServiceAccount Source
}

// Inputs are the per-command values Resolve layers over the file.
type Inputs struct {
	// ProfileFlag is --profile, empty when not given.
	ProfileFlag string
	// ServerFlag is --server, used only when ServerFlagSet.
	ServerFlag    string
	ServerFlagSet bool
	// LookupEnv is os.LookupEnv in production.
	LookupEnv func(string) (string, bool)
}

// Resolved is the effective config for one command.
type Resolved struct {
	Config
	// Profile is the selected profile, empty when none is selected.
	Profile       string
	ProfileSource Source
	Sources       Sources
	// DroppedProfileAuth reports that the server came from a flag or the
	// environment, differs from the profile's, and the profile's auth fields
	// were therefore ignored.
	DroppedProfileAuth bool
}

// UnknownProfileError reports a selected profile the file does not hold.
type UnknownProfileError struct {
	Name string
	// Source is the selection layer that named the profile. Leave it empty
	// when the name is a command argument, which the user can see.
	Source Source
	Known  []string
}

func (e *UnknownProfileError) Error() string {
	from := ""
	if by, ok := map[Source]string{SourceFlag: "--profile", SourceEnv: EnvProfile, SourceProfile: "current_profile"}[e.Source]; ok {
		from = " (from " + by + ")"
	}
	known := "none exist yet; create one with 'cfctl config create NAME --server URL'"
	if len(e.Known) > 0 {
		known = "known profiles: " + strings.Join(e.Known, ", ")
	}
	return fmt.Sprintf("profile %q%s does not exist; %s", e.Name, from, known)
}

// Resolve computes the effective config. The profile is chosen by --profile,
// then CFCTL_PROFILE, then current_profile. Each field then takes the flag,
// then the environment, then the profile. When the server comes from a flag
// or the environment and differs from the profile's, no auth field is taken
// from the profile, so one deployment's credentials never reach another.
// On an *UnknownProfileError the returned Resolved still carries the selected
// name and the flag and environment layers.
func Resolve(f *File, in Inputs) (*Resolved, error) {
	if f == nil {
		f = &File{}
	}
	r := &Resolved{}
	r.Profile, r.ProfileSource = selectProfile(f, in)

	var profile Config
	var unknown error
	if r.Profile != "" {
		p, ok := f.Profiles[r.Profile]
		if !ok {
			unknown = &UnknownProfileError{Name: r.Profile, Source: r.ProfileSource, Known: f.Names()}
		}
		profile = p
	}

	r.Server, r.Sources.Server = profile.Server, sourceOf(profile.Server != "")
	overrideServer := false
	if in.ServerFlagSet {
		r.Server, r.Sources.Server, overrideServer = in.ServerFlag, SourceFlag, true
	} else if v, ok := in.LookupEnv(EnvServer); ok {
		r.Server, r.Sources.Server, overrideServer = v, SourceEnv, true
	}

	// An empty override clears the server, which leaves no deployment to
	// bind the profile's auth to either way.
	if overrideServer && r.Server != "" && !SameServer(r.Server, profile.Server) {
		if profile.hasAuth() {
			r.DroppedProfileAuth = true
		}
		profile = Config{}
	}

	r.IAPAudience, r.Sources.IAPAudience = layerString(profile.IAPAudience, in.LookupEnv, EnvAudience)
	r.ImpersonateServiceAccount, r.Sources.ImpersonateServiceAccount = layerString(profile.ImpersonateServiceAccount, in.LookupEnv, EnvImpersonate)
	r.CredentialsPath, r.Sources.CredentialsPath = layerString(profile.CredentialsPath, in.LookupEnv, EnvCredentials)
	r.ProxyEmail, r.Sources.ProxyEmail = layerString(profile.ProxyEmail, in.LookupEnv, EnvProxyEmail)

	r.AuthNone, r.Sources.AuthNone = profile.AuthNone, sourceOf(profile.AuthNone)
	if v, ok := in.LookupEnv(EnvAuthNone); ok {
		r.Sources.AuthNone = SourceEnv
		r.AuthNone = false
		if v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return r, fmt.Errorf("%s=%q is not a boolean (use true or false)", EnvAuthNone, v)
			}
			r.AuthNone = b
		}
	}
	return r, unknown
}

// selectProfile picks the profile name and its source. An empty selector at
// any layer means unset.
func selectProfile(f *File, in Inputs) (string, Source) {
	if in.ProfileFlag != "" {
		return in.ProfileFlag, SourceFlag
	}
	if v, ok := in.LookupEnv(EnvProfile); ok && v != "" {
		return v, SourceEnv
	}
	if f.CurrentProfile != "" {
		return f.CurrentProfile, SourceProfile
	}
	return "", SourceUnset
}

// hasAuth reports whether any auth field is set.
func (c Config) hasAuth() bool {
	return c.AuthNone || c.ProxyEmail != "" || c.IAPAudience != "" || c.ImpersonateServiceAccount != "" || c.CredentialsPath != ""
}

// layerString returns the environment value when the variable is set, even
// to empty, else the profile value.
func layerString(profile string, lookup func(string) (string, bool), env string) (string, Source) {
	if v, ok := lookup(env); ok {
		return v, SourceEnv
	}
	return profile, sourceOf(profile != "")
}

func sourceOf(set bool) Source {
	if set {
		return SourceProfile
	}
	return SourceUnset
}

// SameServer reports whether a and b name the same deployment: the scheme and
// host compare case-insensitively, and a trailing slash on the path is
// ignored. A URL that does not parse matches only an identical string.
func SameServer(a, b string) bool {
	na, oka := serverKey(a)
	nb, okb := serverKey(b)
	if !oka || !okb {
		return a == b
	}
	return na == nb
}

func serverKey(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/"), true
}
