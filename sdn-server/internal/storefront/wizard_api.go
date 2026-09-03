package storefront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	nodeapi "github.com/spacedatanetwork/sdn-server/internal/api"
)

type storefrontListingBackend struct {
	handler *APIHandler
}

func (backend storefrontListingBackend) service() (*Service, error) {
	if backend.handler == nil || backend.handler.service == nil {
		return nil, errors.New("storefront publication service is unavailable")
	}
	return backend.handler.service, nil
}

func (backend storefrontListingBackend) PublishableInventory(ctx context.Context) (any, error) {
	service, err := backend.service()
	if err != nil {
		return nil, err
	}
	return service.PublishableInventory(ctx)
}

func (backend storefrontListingBackend) OwnListings(ctx context.Context) (any, error) {
	service, err := backend.service()
	if err != nil {
		return nil, err
	}
	return service.OwnListings(ctx)
}

func (backend storefrontListingBackend) PublishListing(ctx context.Context, command nodeapi.StorefrontPublishCommand) (any, error) {
	service, err := backend.service()
	if err != nil {
		return nil, err
	}
	draft, err := decodeListingDraft(command.Listing)
	if err != nil {
		return nil, err
	}
	request := ListingPublicationRequest{
		Listing: draft,
		Publication: PublicationOptions{
			AnnounceTo:    append([]string(nil), command.Publication.AnnounceTo...),
			PinRecords:    command.Publication.PinRecords,
			PinManifest:   command.Publication.PinManifest,
			RetentionDays: command.Publication.RetentionDays,
		},
	}
	if command.Dataset != nil {
		request.Dataset = &DatasetSelection{
			SchemaName: command.Dataset.SchemaName,
			ProviderID: command.Dataset.ProviderID,
			SourceName: command.Dataset.SourceName,
			BatchID:    command.Dataset.BatchID,
		}
	}
	if command.Upload != nil {
		request.Upload = &UploadReference{
			CID:        command.Upload.CID,
			SHA256:     command.Upload.SHA256,
			ByteLength: command.Upload.ByteLength,
			FileName:   command.Upload.FileName,
			MediaType:  command.Upload.MediaType,
		}
	}
	report, err := service.PublishListing(ctx, request)
	if err != nil {
		return nil, err
	}
	if pinErr := backend.pinPublicationArtifacts(ctx, report, request.Publication); pinErr != nil {
		if report.PropagationError != "" {
			report.PropagationError += "; "
		}
		report.PropagationError += pinErr.Error()
	}
	return report, nil
}

func (backend storefrontListingBackend) pinPublicationArtifacts(ctx context.Context, report *ListingPropagationReport, options PublicationOptions) error {
	if report == nil || (!options.PinRecords && !options.PinManifest) {
		return nil
	}
	if backend.handler == nil || backend.handler.delivery == nil || backend.handler.service == nil || backend.handler.service.store == nil {
		return errors.New("pin signed publication: IPFS pinning is unavailable on this node")
	}
	publication, err := backend.handler.service.store.GetListingPublication(report.ListingID)
	if err != nil {
		return fmt.Errorf("pin signed publication: %w", err)
	}
	if publication == nil {
		return errors.New("pin signed publication: publication artifacts were not found")
	}
	artifacts := []struct {
		enabled bool
		name    string
		data    []byte
	}{
		{enabled: options.PinRecords, name: report.ListingID + ".stf", data: publication.STFBytes},
		{enabled: options.PinRecords, name: report.ListingID + ".pnm", data: publication.PNMBytes},
		{enabled: options.PinManifest && len(publication.DPMBytes) > 0, name: report.ListingID + ".dpm", data: publication.DPMBytes},
	}
	for _, artifact := range artifacts {
		if !artifact.enabled {
			continue
		}
		result, deliverErr := backend.handler.delivery.Deliver(ctx, &DeliveryRequest{
			Method: DeliveryIPFSPin, Data: artifact.data, IPFSPinName: artifact.name,
		})
		if deliverErr != nil {
			return fmt.Errorf("pin signed publication: %w", deliverErr)
		}
		if result == nil || !result.Success || strings.TrimSpace(result.CID) == "" {
			return errors.New("pin signed publication: node returned no CID")
		}
	}
	return nil
}

func decodeListingDraft(raw json.RawMessage) (ListingDraft, error) {
	var draft ListingDraft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return ListingDraft{}, fmt.Errorf("invalid SDS listing draft: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ListingDraft{}, errors.New("invalid SDS listing draft: trailing JSON value")
	}
	return draft, nil
}

func (backend storefrontListingBackend) WithdrawListing(ctx context.Context, listingID string) (any, error) {
	service, err := backend.service()
	if err != nil {
		return nil, err
	}
	listing, err := service.WithdrawOwnListing(ctx, listingID)
	if err != nil {
		return nil, err
	}
	if backend.handler.catalog != nil {
		backend.handler.catalog.RemoveListing(ctx, listingID)
	}
	return listing, nil
}

func (backend storefrontListingBackend) PinUpload(ctx context.Context, upload nodeapi.StorefrontUpload) (any, error) {
	if backend.handler == nil || backend.handler.delivery == nil {
		return nil, errors.New("IPFS pinning is unavailable on this node")
	}
	if len(upload.Data) == 0 {
		return nil, errors.New("upload is empty")
	}
	result, err := backend.handler.delivery.Deliver(ctx, &DeliveryRequest{
		Method:      DeliveryIPFSPin,
		Data:        upload.Data,
		IPFSPinName: upload.FileName,
	})
	if err != nil {
		return nil, fmt.Errorf("pin upload: %w", err)
	}
	if result == nil || !result.Success || strings.TrimSpace(result.CID) == "" {
		return nil, errors.New("pin upload: node returned no CID")
	}
	digest := sha256.Sum256(upload.Data)
	return nodeapi.StorefrontUploadReference{
		CID: result.CID, SHA256: hex.EncodeToString(digest[:]),
		ByteLength: int64(len(upload.Data)), FileName: upload.FileName, MediaType: upload.MediaType,
	}, nil
}
