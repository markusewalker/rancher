package clusterauthtoken

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rancher/rancher/pkg/auth/tokens"
	mgmtcontrollers "github.com/rancher/rancher/pkg/generated/controllers/management.cattle.io/v3"
	clusterv3 "github.com/rancher/rancher/pkg/generated/norman/cluster.cattle.io/v3"
	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type clusterAuthTokenHandler struct {
	tokenCache  mgmtcontrollers.TokenCache
	tokenClient mgmtcontrollers.TokenClient
}

// Sync ClusterAuthToken back to Token.
func (h *clusterAuthTokenHandler) sync(key string, clusterAuthToken *clusterv3.ClusterAuthToken) (runtime.Object, error) {
	if clusterAuthToken == nil || clusterAuthToken.DeletionTimestamp != nil {
		return nil, nil
	}

	if !clusterAuthToken.Enabled ||
		isExpired(clusterAuthToken) ||
		clusterAuthToken.LastUsedAt == nil {
		return clusterAuthToken, nil // Nothing to do.
	}

	tokenName := clusterAuthToken.Name
	token, err := h.tokenCache.Get(tokenName)
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
			return clusterAuthToken, nil // ClusterAuthToken was orphaned.
		}
		return nil, fmt.Errorf("error getting token %s: %w", tokenName, err)
	}

	if token.LastUsedAt != nil && token.LastUsedAt.After(clusterAuthToken.LastUsedAt.Time) {
		return clusterAuthToken, nil // Nothing to do.
	}

	if tokens.IsExpired(*token) {
		return clusterAuthToken, nil // Should not update expired token.
	}

	if err := func() error {
		patch, err := json.Marshal([]struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		}{{
			Op:    "replace",
			Path:  "/lastUsedAt",
			Value: clusterAuthToken.LastUsedAt,
		}})
		if err != nil {
			return err
		}

		_, err = h.tokenClient.Patch(token.Name, types.JSONPatchType, patch)
		return err
	}(); err != nil {
		return nil, fmt.Errorf("error updating lastUsedAt for token %s: %v", tokenName, err)
	}

	logrus.Debugf("[%s] Updated lastUsedAt for token %s", clusterAuthTokenController, tokenName)

	return clusterAuthToken, nil
}

func isExpired(t *clusterv3.ClusterAuthToken) bool {
	if t.ExpiresAt == "" {
		return false
	}

	expiresAt, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return false
	}

	return time.Now().After(expiresAt)
}
