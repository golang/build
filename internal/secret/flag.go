// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package secret

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// FlagResolver contains the dependencies necessary to resolve a Secret flag.
type FlagResolver struct {
	Context          context.Context
	Client           secretClient
	DefaultProjectID string
}

const secretSuffix = "[ specify `secret:[project name/]<secret name>` to read from Secret Manager ]"

// Flag defines a string flag on set that will be resolved using r.
// The return value is the address of a string variable that stores the value of the flag.
func (r *FlagResolver) Flag(set *flag.FlagSet, name, usage string) *string {
	p := new(string)
	r.FlagVar(set, p, name, usage)
	return p
}

// FlagVar defines a string flag on set that will be resolved using r.
// The argument p points to a string variable in which to store the value of the flag.
func (r *FlagResolver) FlagVar(set *flag.FlagSet, p *string, name, usage string) {
	suffixedUsage := usage + "\n" + secretSuffix
	set.Func(name, suffixedUsage, func(flagValue string) error {
		value, err := r.ResolveSecret(flagValue)
		if err != nil {
			return err
		}
		*p = value
		return nil
	})
}

// ResolveSecret resolves a string value, which need not be a flag.
func (r *FlagResolver) ResolveSecret(flagValue string) (string, error) {
	if r.Client == nil || r.Context == nil {
		return "", fmt.Errorf("secret resolver was not initialized")
	}
	if !strings.HasPrefix(flagValue, "secret:") {
		return flagValue, nil
	}

	secretName := strings.TrimPrefix(flagValue, "secret:")
	projectID := r.DefaultProjectID
	if parts := strings.SplitN(secretName, "/", 2); len(parts) == 2 {
		projectID, secretName = parts[0], parts[1]
	}
	if projectID == "" {
		return "", fmt.Errorf("missing project ID: none specified in %q, and no default set (not on GCP?)", secretName)
	}
	result, err := r.Client.AccessSecretVersion(r.Context, &secretmanagerpb.AccessSecretVersionRequest{
		Name: buildNamePath(projectID, secretName, "latest"),
	})
	if err != nil {
		return "", fmt.Errorf("reading secret %q from project %v failed: %v", secretName, projectID, err)
	}
	return string(result.Payload.GetData()), nil
}

// JSONVarFlag defines a flag on set that behaves like Flag and then
// json.Unmarshals the resulting string into value.
func (r *FlagResolver) JSONVarFlag(set *flag.FlagSet, value interface{}, name, usage string) {
	suffixedUsage := usage + "\n" + fmt.Sprintf("A JSON representation of a %T.", value) + "\n" + secretSuffix
	set.Func(name, suffixedUsage, func(flagValue string) error {
		stringValue, err := r.ResolveSecret(flagValue)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(stringValue), value)
	})
}

// DefaultResolver is the FlagResolver used by the convenience functions.
var DefaultResolver FlagResolver

// Flag defines a string flag on flag.CommandLine that supports Secret Manager
// resolution for values like "secret:<secret name>". InitFlagSupport must be
// called before flag.Parse.
func Flag(name, usage string) *string {
	return DefaultResolver.Flag(flag.CommandLine, name, usage)
}

// FlagVar defines a string flag on flag.CommandLine that supports Secret Manager
// resolution for values like "secret:<secret name>". InitFlagSupport must be
// called before flag.Parse.
func FlagVar(p *string, name, usage string) {
	DefaultResolver.FlagVar(flag.CommandLine, p, name, usage)
}

// JSONVarFlag defines a flag on flag.CommandLine that behaves like Flag
// and then json.Unmarshals the resulting string into value.
func JSONVarFlag(value interface{}, name, usage string) {
	DefaultResolver.JSONVarFlag(flag.CommandLine, value, name, usage)
}

// InitFlagSupport initializes the dependencies for flags declared with Flag.
func InitFlagSupport(ctx context.Context) error {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return err
	}
	DefaultResolver = FlagResolver{
		Context: ctx,
		Client:  client,
	}
	if metadata.OnGCE() {
		projectID, err := metadata.ProjectID()
		if err != nil {
			return err
		}
		DefaultResolver.DefaultProjectID = projectID
	}

	return nil
}
