package typesafe

import (
	"context"
	"errors"
	"net/http"
)

// ModelsService lists models; reach it through [Client.Models].
type ModelsService struct {
	client *Client
}

// List returns the models available to the account. Pass a returned name to
// [WithModel]. [WithExtraBody] is rejected.
func (s *ModelsService) List(ctx context.Context, opts ...CallOption) (*ListModelsResponse, error) {
	c := s.client
	o := resolveCallOptions(opts)
	if o.extraBody != nil {
		return nil, errors.New("typesafe: WithExtraBody is not supported by Models.List")
	}
	req, err := c.prepare(http.MethodGet, modelsPath, nil, o)
	if err != nil {
		return nil, err
	}
	var out *ListModelsResponse
	err = c.send(ctx, req, func(resp *http.Response, raw []byte) error {
		r, err := decodeModels(raw)
		if err != nil {
			return newValidationError(resp, raw, req.endpoint, err)
		}
		r.ResponseMeta = newMeta(resp, raw)
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
