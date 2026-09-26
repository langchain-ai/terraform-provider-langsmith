# Import by workspace ID. Refresh discovers the data plane URL for updates/deletes.
terraform import langsmith_workspace.example workspace-id

# Include the data plane ID when data_plane_id is set in the resource configuration.
terraform import langsmith_workspace.byoc workspace-id/00000000-0000-0000-0000-000000000001
