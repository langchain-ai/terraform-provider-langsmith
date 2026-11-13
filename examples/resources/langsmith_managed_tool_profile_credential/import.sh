# Imported credentials have no values in state; the next apply rewrites them from configuration.
terraform import langsmith_managed_tool_profile_credential.agent '00000000-0000-0000-0000-000000000004/00000000-0000-0000-0000-000000000005'
terraform import langsmith_managed_tool_profile_credential.agent '00000000-0000-0000-0000-000000000001/00000000-0000-0000-0000-000000000004/00000000-0000-0000-0000-000000000005'
