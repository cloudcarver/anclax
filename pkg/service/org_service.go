package service

import (
	"context"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

func orgToApigen(org *querier.AnclaxOrg) *apigen.Org {
	return &apigen.Org{
		ID:        org.ID,
		Name:      org.Name,
		CreatedAt: org.CreatedAt,
		UpdatedAt: org.UpdatedAt,
	}
}

func (s *Service) ListOrgs(ctx context.Context, id int32) ([]apigen.Org, error) {
	orgs, err := s.m.ListOrgs(ctx, id)
	if err != nil {
		return nil, err
	}

	ret := make([]apigen.Org, len(orgs))
	for i, org := range orgs {
		ret[i] = *orgToApigen(org)
	}

	return ret, nil
}
