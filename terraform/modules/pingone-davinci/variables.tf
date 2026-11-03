# Input variables mirror tfvars schema v1. The Go package internal/tfvars is
# the authoritative definition (schema.go); keep the two in step. Attribute
# names inside nested objects match the pingidentity/pingone 1.23 schema
# wherever a value is passed straight through.

variable "tfvars_schema_version" {
  description = "Schema version of the environment tfvars file. Only 1 is supported."
  type        = number

  validation {
    condition     = var.tfvars_schema_version == 1
    error_message = "This module version supports tfvars_schema_version = 1 only."
  }
}

variable "pingone_environment_id" {
  description = "PingOne environment that owns every managed DaVinci resource."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.pingone_environment_id))
    error_message = "pingone_environment_id must be a lowercase UUID."
  }
}

variable "pingone_secrets" {
  description = "Secret values by key. The root stack builds this map from one sensitive variable per secret (pipeline TF_VAR_<key>); null values count as missing. Never put these in tfvars."
  type        = map(string)
  default     = {}
  nullable    = false
  sensitive   = true

  validation {
    # Each key is also a root variable name fed by TF_VAR_<key>.
    condition     = alltrue([for k in nonsensitive(keys(var.pingone_secrets)) : can(regex("^[A-Za-z][A-Za-z0-9_]{0,127}$", k))])
    error_message = "pingone_secrets keys must match ^[A-Za-z][A-Za-z0-9_]{0,127}$ (a Terraform variable name)."
  }
}

variable "parameters" {
  description = "Environment-local, non-secret values referenced as @@param:<key>@@."
  type        = map(string)
  default     = {}
  nullable    = false

  validation {
    condition     = alltrue([for k in keys(var.parameters) : can(regex("^[a-z0-9_]{1,128}$", k))])
    error_message = "parameters keys must match ^[a-z0-9_]{1,128}$."
  }
}

variable "davinci_connections" {
  description = "DaVinci connector instances by logical key."
  type = map(object({
    name       = string
    connector  = string
    properties = optional(string)
  }))
  default  = {}
  nullable = false

  validation {
    condition     = alltrue([for k in keys(var.davinci_connections) : can(regex("^[A-Za-z0-9_-]{1,128}$", k))])
    error_message = "davinci_connections keys must match ^[A-Za-z0-9_-]{1,128}$."
  }
}

variable "davinci_variables" {
  description = "DaVinci variables by logical key (<name>_<context>[_<flowKey>])."
  type = map(object({
    name         = string
    context      = string
    data_type    = string
    mutable      = bool
    display_name = optional(string)
    flow         = optional(string)
    min          = optional(number)
    max          = optional(number)
    secret       = optional(string)
    value = optional(object({
      string      = optional(string)
      bool        = optional(bool)
      float32     = optional(number)
      json_object = optional(string)
    }))
  }))
  default  = {}
  nullable = false

  validation {
    condition     = alltrue([for k in keys(var.davinci_variables) : can(regex("^[A-Za-z0-9_-]{1,128}$", k))])
    error_message = "davinci_variables keys must match ^[A-Za-z0-9_-]{1,128}$."
  }
  validation {
    condition     = alltrue([for v in values(var.davinci_variables) : contains(["company", "flow", "flowInstance", "user"], v.context)])
    error_message = "davinci_variables context must be one of company, flow, flowInstance, user."
  }
  validation {
    condition     = alltrue([for v in values(var.davinci_variables) : contains(["boolean", "number", "object", "secret", "string"], v.data_type)])
    error_message = "davinci_variables data_type must be one of boolean, number, object, secret, string."
  }
}

variable "davinci_flows" {
  description = "DaVinci flows by logical key. tier (0..3) orders subflow references."
  type = map(object({
    name        = string
    description = optional(string)
    color       = optional(string)
    tier        = number
    enabled     = optional(bool, true)
    deploy      = optional(bool, true)
    settings = optional(object({
      csp                                 = optional(string)
      css                                 = optional(string)
      css_links                           = optional(set(string))
      custom_error_screen_brand_logo_url  = optional(string)
      custom_error_show_footer            = optional(bool)
      custom_favicon_link                 = optional(string)
      custom_logo_urlselection            = optional(number)
      custom_timeout_error_screen_css     = optional(string)
      custom_timeout_error_screen_html    = optional(string)
      custom_timeout_error_screen_message = optional(string)
      custom_title                        = optional(string)
      default_error_screen_brand_logo     = optional(bool)
      flow_http_timeout_in_seconds        = optional(number)
      flow_timeout_in_seconds             = optional(number)
      intermediate_loading_screen_css     = optional(string)
      intermediate_loading_screen_html    = optional(string)
      js_custom_flow_player               = optional(string)
      js_links = optional(set(object({
        crossorigin    = string
        defer          = bool
        integrity      = string
        label          = string
        referrerpolicy = string
        type           = string
        value          = string
      })))
      log_level                          = optional(number)
      preview_form_rendering_updates     = optional(bool)
      require_authentication_to_initiate = optional(bool)
      scrub_sensitive_info               = optional(bool)
      sensitive_info_fields              = optional(set(string))
      use_csp                            = optional(bool)
      use_custom_css                     = optional(bool)
      use_custom_flow_player             = optional(bool)
      use_custom_script                  = optional(bool)
      use_custom_timeout_error_screen    = optional(bool)
      use_intermediate_loading_screen    = optional(bool)
      validate_on_save                   = optional(bool)
    }))
    trigger = optional(object({
      type    = string
      subtype = optional(string)
      configuration = optional(object({
        mfa = object({ enabled = bool, time = number, time_format = string })
        pwd = object({ enabled = bool, time = number, time_format = string })
      }))
    }))
    input_schema = optional(list(object({
      description            = optional(string)
      is_expanded            = optional(bool)
      preferred_control_type = optional(string)
      preferred_data_type    = optional(string)
      property_name          = optional(string)
      required               = optional(bool)
    })))
    output_schema = optional(object({
      output = object({
        type                  = string
        properties            = optional(string)
        additional_properties = optional(bool)
      })
    }))
    graph = object({
      box_selection_enabled = optional(bool)
      data                  = optional(string)
      max_zoom              = optional(number)
      min_zoom              = optional(number)
      pan                   = optional(object({ x = number, y = number }))
      panning_enabled       = optional(bool)
      renderer              = optional(string)
      user_panning_enabled  = optional(bool)
      user_zooming_enabled  = optional(bool)
      zoom                  = optional(number)
      zooming_enabled       = optional(bool)
      nodes = map(object({
        node_type        = string
        connection       = optional(string)
        connector_id     = optional(string)
        capability_name  = optional(string)
        capability_class = optional(string)
        label            = optional(string)
        status           = optional(string)
        type             = optional(string)
        properties       = optional(string)
        outcomes         = optional(list(object({ id = string, label = string, result = string })))
        position         = optional(object({ x = number, y = number }))
        classes          = optional(string)
        group            = optional(string)
        grabbable        = optional(bool)
        locked           = optional(bool)
        pannable         = optional(bool)
        removed          = optional(bool)
        selectable       = optional(bool)
        selected         = optional(bool)
      }))
      edges = map(object({
        source                = string
        target                = string
        multi_value_source_id = optional(string)
        position              = optional(object({ x = number, y = number }))
        classes               = optional(string)
        group                 = optional(string)
        grabbable             = optional(bool)
        locked                = optional(bool)
        pannable              = optional(bool)
        removed               = optional(bool)
        selectable            = optional(bool)
        selected              = optional(bool)
      }))
    })
  }))
  default  = {}
  nullable = false

  validation {
    condition     = alltrue([for k in keys(var.davinci_flows) : can(regex("^[A-Za-z0-9_-]{1,128}$", k))])
    error_message = "davinci_flows keys must match ^[A-Za-z0-9_-]{1,128}$."
  }
  validation {
    condition     = alltrue([for f in values(var.davinci_flows) : contains([0, 1, 2, 3], f.tier)])
    error_message = "davinci_flows tier must be 0, 1, 2 or 3."
  }
}

variable "davinci_applications" {
  description = "DaVinci applications by logical key, each with its flow policies."
  type = map(object({
    name            = string
    api_key_enabled = optional(bool)
    oauth = optional(object({
      enforce_signed_request_openid = optional(bool)
      grant_types                   = optional(set(string))
      logout_uris                   = optional(set(string))
      redirect_uris                 = optional(set(string))
      scopes                        = optional(set(string))
      sp_jwks_openid                = optional(string)
      sp_jwks_url                   = optional(string)
    }))
    flow_policies = optional(map(object({
      name   = string
      status = optional(string)
      trigger = optional(object({
        type = optional(string)
        configuration = optional(object({
          mfa = optional(object({ enabled = optional(bool), time = optional(number), time_format = optional(string) }))
          pwd = optional(object({ enabled = optional(bool), time = optional(number), time_format = optional(string) }))
        }))
      }))
      distributions = list(object({
        flow          = string
        version       = number
        weight        = optional(number)
        ip            = optional(set(string))
        success_nodes = optional(set(string))
      }))
    })), {})
  }))
  default  = {}
  nullable = false

  validation {
    condition     = alltrue([for k in keys(var.davinci_applications) : can(regex("^[A-Za-z0-9_-]{1,128}$", k))])
    error_message = "davinci_applications keys must match ^[A-Za-z0-9_-]{1,128}$."
  }
}

variable "import_ids" {
  description = "Existing PingOne IDs to adopt, by logical key. Flow policies use \"<app_key>/<policy_key>\" = \"<app_id>/<policy_id>\"."
  type = object({
    davinci_connections   = optional(map(string), {})
    davinci_flows         = optional(map(string), {})
    davinci_variables     = optional(map(string), {})
    davinci_applications  = optional(map(string), {})
    davinci_flow_policies = optional(map(string), {})
  })
  default  = {}
  nullable = false
}
