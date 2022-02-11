package secret

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

// FlagResolver contains the dependencies necessary to resolve a Secret flag.
type FlagResolver struct {
	Context          context.Context
	Client           secretClient
	DefaultProjectID string
}

// Flag declares a string flag on set that will be resolved using r.
func (r *FlagResolver) Flag(set *flag.FlagSet, name string, usage string) *string {
	var value string
	suffixedUsage := usage + " [ specify `secret:[project name/]<secret name>` to read from Secret Manager ]"
	set.Func(name, suffixedUsage, func(flagValue string) error {
		if r.Client == nil || r.Context == nil {
			return fmt.Errorf("secret resolver was not initialized")
		}
		if !strings.HasPrefix(flagValue, "secret:") {
			value = flagValue
			return nil
		}

		secretName := strings.TrimPrefix(flagValue, "secret:")
		projectID := r.DefaultProjectID
		if parts := strings.SplitN(secretName, "/", 2); len(parts) == 2 {
			projectID, secretName = parts[0], parts[1]
		}
		if projectID == "" {
			return fmt.Errorf("missing project ID: none specified in %q, and no default set (not on GCP?)", secretName)
		}
		r, err := r.Client.AccessSecretVersion(r.Context, &secretmanagerpb.AccessSecretVersionRequest{
			Name: buildNamePath(projectID, secretName, "latest"),
		})
		if err != nil {
			return fmt.Errorf("reading secret %q from project %v failed: %v", secretName, projectID, err)
		}
		value = string(r.Payload.GetData())
		return nil
	})
	return &value
}

// DefaultResolver is the FlagResolver used by the convenience functions.
var DefaultResolver FlagResolver

// Flag declares a string flag on flag.CommandLine that supports Secret Manager
// resolution for values like "secret:<secret name>". InitFlagSupport must be
// called before flag.Parse.
func Flag(name string, usage string) *string {
	return DefaultResolver.Flag(flag.CommandLine, name, usage)
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
