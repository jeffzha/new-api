# Model compatibility registry and Claude prompt caching

This document describes the operator-maintained compatibility layer used when a provider accepts an OpenAI-compatible request but implements a model parameter differently. The registry is deliberately small, deterministic, versioned, and independent of channel parameter overrides so it remains easy to merge with upstream new-api releases.

## Processing order

For OpenAI Chat, OpenAI Responses, and Claude Messages requests, the gateway applies request processing in this order:

1. parse and validate the downstream request;
2. map the downstream model name to the upstream model name;
3. remove fields disabled by the channel;
4. apply the active model compatibility registry version;
5. apply the channel's existing parameter override rules;
6. convert the request to the upstream protocol and send it.

Channel parameter overrides intentionally remain last. An administrator can therefore override a registry result for a specific channel without changing the shared registry.

Every registry change applied to a request is appended to the existing parameter-override audit field in the usage log. The entry contains the active registry version, profile, action, and parameter path. A scalar mapping can also record a compact transition such as `low->high`; prompts, messages, tool definitions, and arbitrary request content are never copied into this audit.

## Built-in production baseline

The built-in registry contains the verified DeepSeek V4 effort mappings for channel type 43:

- OpenAI Chat and Responses: `reasoning_effort` and `reasoning.effort` map `low`/`medium` to `high`, and `xhigh` to `max`.
- Claude Messages: `output_config.effort` applies the same mapping.

Missing parameters are left missing. Explicit `0`, `false`, empty strings, and supported values are preserved. Rules do not modify model names, prompts, messages, tools, metadata, user identifiers, streaming fields, or cache-control fields.

## Registry schema

The complete registry is stored in the system option `model_compatibility.registry`.

```json
{
  "schema_version": 1,
  "enabled": true,
  "active_version": "production-2026-08-21",
  "versions": [
    {
      "id": "production-2026-08-21",
      "description": "Verified production mappings",
      "source": "operator",
      "profiles": [
        {
          "id": "deepseek-v4-canary",
          "channel_types": [43],
          "channel_ids": [7],
          "groups": ["compatibility-canary"],
          "model_regex": "^deepseek-v4-(flash|pro)$",
          "relay_formats": ["openai", "openai_responses"],
          "rules": [
            {
              "path": "reasoning_effort",
              "action": "map",
              "values": {
                "low": "high",
                "medium": "high",
                "xhigh": "max"
              }
            }
          ]
        }
      ]
    }
  ]
}
```

Profile scope fields are intersected:

- `channel_types` is required and identifies provider adaptor types;
- `channel_ids` is optional and supports a channel-level canary;
- `groups` is optional and supports a customer-group canary;
- `model_regex` matches the mapped upstream model name;
- `relay_formats` supports `openai`, `openai_responses`, and `claude`;
- `disabled` can temporarily disable one profile without deleting it.

An empty optional scope means all values in that dimension. A request must match every non-empty scope.

Supported actions:

- `drop`: remove a parameter only when it exists;
- `inject_if_absent`: add a default only when the parameter is absent, preserving explicit zero and false values;
- `map`: replace an exact scalar value using `values`;
- `rename`: move a parameter only when the destination is absent;
- `reject`: return a deterministic HTTP 400 for a known unsupported parameter.

Rules can include up to eight `conditions`, using `equals`, `not_equals`, `exists`, or `not_exists`. The registry is bounded to 20 versions, 256 profiles per version, and 256 rules per profile. Invalid JSON, duplicate IDs, invalid regular expressions, unsupported actions, missing active versions, or unsafe parameter paths are rejected before persistence.

## Maintenance and rollout

Use **System Settings → Models → Model Compatibility Registry**.

1. Copy the current active version object within the JSON editor.
2. Give the copy a new immutable ID, such as `canary-2026-08-22-01`.
3. Add or modify profiles in the new version. Initially restrict the profile with `channel_ids` or `groups`.
4. Save while `active_version` still points to the old version. This validates and stores the draft without changing traffic.
5. Select the new version in **Active Registry Version** and save. Activation is a single option update.
6. Test the canary channel/group and review request errors, upstream errors, and the parameter-override audit in usage logs.
7. Expand the scope by creating another version; do not rewrite the version that produced historical audit entries.
8. To roll back, select the previous active version and save. To stop all registry processing, turn off **Enable Compatibility Rules**.

Provider model discovery can later propose a draft version, but it must never activate or overwrite production rules automatically. A provider's model list does not prove parameter semantics, and model aliases can change without notice. Operators should record the official documentation or verified request used as the version's `source`/`description`, then canary it before activation.

## Claude prompt caching

Use **System Settings → Models → Claude** to configure:

- **Automatic Claude Prompt Caching** (`claude.prompt_cache_enabled`), disabled by default;
- **Prompt Cache TTL** (`claude.prompt_cache_ttl`), either `5m` or `1h`.

The gateway only auto-adds native top-level Claude cache control when all of these are true:

- the source protocol is OpenAI Chat or OpenAI Responses;
- the selected channel is the native Anthropic channel type;
- request-body pass-through is disabled;
- automatic caching is enabled.

The gateway does not auto-enable caching for Bedrock, Vertex, generic OpenAI-compatible providers, native Claude requests, or pass-through requests. Explicit top-level and block-level cache controls supplied by the client are preserved during conversion. Automatic caching is skipped when four explicit breakpoints already exist or when an explicit breakpoint uses a different TTL, avoiding provider breakpoint-limit and mixed-TTL errors.

The default 5-minute cache has the lower write price. The 1-hour cache has a higher cache-write price, so enable it only after the corresponding Claude cache creation/read ratios are configured and verified in billing logs.

## Failure and rollback behavior

- Invalid registry configuration is rejected by the option API and the previous version remains active.
- A runtime registry parse failure stops the request before it reaches the provider; it does not silently bypass policy.
- A `reject` rule returns HTTP 400 and is not retried on another channel.
- Compatibility rules never alter protected request content.
- Disabling the registry or selecting the prior version takes effect for subsequent requests without restarting the gateway.
