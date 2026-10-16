package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const dataPlanesPath = "api/v1/orgs/current/data-planes"

var (
	_             resource.Resource                   = &DataPlaneResource{}
	_             resource.ResourceWithImportState    = &DataPlaneResource{}
	_             resource.ResourceWithValidateConfig = &DataPlaneResource{}
	dataPlaneUUID                                     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type DataPlaneResource struct {
	client       *langsmith.Client
	pollInterval time.Duration
}

func NewDataPlaneResource() resource.Resource { return &DataPlaneResource{} }

func (r *DataPlaneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data_plane"
}

func (r *DataPlaneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *DataPlaneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dataPlaneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.create(ctx, plan)
	if model.ID.ValueString() != "" {
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Data Plane", err.Error())
	}
}

func (r *DataPlaneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dataPlaneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	api, err := r.get(ctx, state.ID.ValueString())
	if isLangSmithNotFound(err) || (err == nil && api.Status == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Data Plane", err.Error())
		return
	}
	model, err := dataPlaneModelFromAPI(api, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Decode LangSmith Data Plane", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *DataPlaneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan dataPlaneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.update(ctx, state, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Data Plane", err.Error())
	}
}

func (r *DataPlaneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dataPlaneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.delete(ctx, state); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Data Plane", err.Error())
	}
}

func (r *DataPlaneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !dataPlaneUUID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid Data Plane Import ID", "Import using the data plane UUID in the provider's current organization.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(req.ID))...)
}

func dataPlanePath(id string) string { return dataPlanesPath + "/" + url.PathEscape(id) }

func (r *DataPlaneResource) get(ctx context.Context, id string) (dataPlaneAPI, error) {
	var api dataPlaneAPI
	if !dataPlaneUUID.MatchString(id) {
		return api, errors.New("invalid data plane UUID")
	}
	if err := r.client.Get(ctx, dataPlanePath(id), nil, &api); err != nil {
		return api, err
	}
	if api.ID != id || api.Status == "" {
		return dataPlaneAPI{}, errors.New("data plane response has a missing or mismatched identity or status")
	}
	return api, nil
}

func (r *DataPlaneResource) create(ctx context.Context, plan dataPlaneModel) (dataPlaneModel, error) {
	timeout, err := dataPlaneTimeout(plan.Timeouts, "create")
	if err != nil {
		return dataPlaneModel{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var api dataPlaneAPI
	if err := r.client.Post(ctx, dataPlanesPath, dataPlaneCreatePayload(plan), &api, option.WithMaxRetries(0)); err != nil {
		return dataPlaneModel{}, err
	}
	if !dataPlaneUUID.MatchString(api.ID) {
		return dataPlaneModel{}, errors.New("LangSmith did not return a valid data plane UUID")
	}
	// Keep the identity even if decoding or the first poll fails after POST.
	model, err := dataPlaneModelFromAPI(api, plan)
	if err != nil {
		// Malformed settings must not strand the successfully created identity
		// or leave planned unknown values in the error state.
		fallback, _ := dataPlaneModelFromAPI(dataPlaneAPI{ID: api.ID, Name: api.Name, Region: api.Region, Status: api.Status}, plan)
		return fallback, err
	}
	model, err = r.wait(ctx, model, false)
	if err != nil {
		return model, err
	}
	return r.patchAndWait(ctx, model, dataPlaneUpdatePayload(model, plan))
}

func (r *DataPlaneResource) update(ctx context.Context, state, plan dataPlaneModel) (dataPlaneModel, error) {
	timeout, err := dataPlaneTimeout(plan.Timeouts, "update")
	if err != nil {
		return state, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Wait out a previously accepted update before issuing another PATCH.
	current, err := r.wait(ctx, state, false)
	if err != nil {
		return current, err
	}
	current.Timeouts = plan.Timeouts
	return r.patchAndWait(ctx, current, dataPlaneUpdatePayload(current, plan))
}

func (r *DataPlaneResource) patchAndWait(ctx context.Context, current dataPlaneModel, payload map[string]any) (dataPlaneModel, error) {
	if len(payload) == 0 {
		return current, nil
	}
	var api dataPlaneAPI
	if err := r.client.Patch(ctx, dataPlanePath(current.ID.ValueString()), payload, &api, option.WithMaxRetries(0)); err != nil {
		return current, err
	}
	if api.ID != current.ID.ValueString() {
		return current, errors.New("update response has a missing or mismatched data plane UUID")
	}
	model, err := dataPlaneModelFromAPI(api, current)
	if err != nil {
		return current, err
	}
	return r.wait(ctx, model, false)
}

func (r *DataPlaneResource) delete(ctx context.Context, state dataPlaneModel) error {
	timeout, err := dataPlaneTimeout(state.Timeouts, "delete")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		api, err := r.get(ctx, state.ID.ValueString())
		if isLangSmithNotFound(err) || (err == nil && api.Status == "deleted") {
			return nil
		}
		if err != nil {
			return err
		}
		switch api.Status {
		case "deprovisioning":
			_, err := r.wait(ctx, state, true)
			return err
		case "active", "provisioning_failed":
			if err := r.client.Delete(ctx, dataPlanePath(api.ID), nil, nil, option.WithMaxRetries(0)); err != nil {
				if isLangSmithNotFound(err) {
					return nil
				}
				return err
			}
			_, err := r.wait(ctx, state, true)
			return err
		case "requested", "provisioning", "updating":
			// The API cannot delete these states. Let the current operation finish.
			if err := r.pause(ctx); err != nil {
				return fmt.Errorf("waiting for data plane %s to become deletable (status %s): %w", api.ID, api.Status, err)
			}
		default:
			return fmt.Errorf("data plane %s cannot be deleted in status %q; resolve its lifecycle status before retrying", api.ID, api.Status)
		}
	}
}

func (r *DataPlaneResource) wait(ctx context.Context, current dataPlaneModel, deleting bool) (dataPlaneModel, error) {
	for {
		api, err := r.get(ctx, current.ID.ValueString())
		if deleting && isLangSmithNotFound(err) {
			return current, nil
		}
		if err != nil {
			return current, fmt.Errorf("waiting for data plane %s (last status %s): %w", current.ID.ValueString(), current.Status.ValueString(), err)
		}
		next, err := dataPlaneModelFromAPI(api, current)
		if err != nil {
			return current, err
		}
		current = next
		if deleting {
			if api.Status == "deleted" {
				return current, nil
			}
			if api.Status != "deprovisioning" {
				return current, fmt.Errorf("data plane %s entered unexpected status %q during deletion", api.ID, api.Status)
			}
		} else {
			switch api.Status {
			case "active":
				return current, nil
			case "requested", "provisioning", "updating":
			default:
				return current, fmt.Errorf("data plane %s entered status %q while waiting for active", api.ID, api.Status)
			}
		}
		if err := r.pause(ctx); err != nil {
			return current, fmt.Errorf("waiting for data plane %s (last status %s): %w", api.ID, api.Status, err)
		}
	}
}

func (r *DataPlaneResource) pause(ctx context.Context) error {
	interval := r.pollInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dataPlaneTimeout(timeouts types.Object, operation string) (time.Duration, error) {
	fallback := 2 * time.Hour
	if operation == "update" {
		fallback = time.Hour
	}
	value, ok := timeouts.Attributes()[operation].(types.String)
	if !ok || value.IsNull() || value.IsUnknown() {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value.ValueString())
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("timeouts.%s must be a positive duration such as 2h or 30m", operation)
	}
	return duration, nil
}
