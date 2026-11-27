# Logins

A Login is a named credential that runs use. It has two parts:

- A `Login` object in the agents namespace. It holds the metadata: kind,
  repositories, egress and a description. Anyone with `runs:read` can list it.
- A Secret with the same name in the logins namespace (`agent-logins` by
  default). It holds the value. The API never returns the value.

Only the owner writes Logins. Agent tokens never get the `logins:write` scope.

## Kinds

| Kind | Secret key | Used by | Value |
|---|---|---|---|
| `github-token` | `token` | `AgentProfile.spec.github` | A GitHub token. `repos` lists the repositories it may be used for. |
| `model-api-key` | `api-key` | `HarnessProfile.spec.login` | The API key of a model endpoint. |
| `claude-credentials` | `credentials.json` | `HarnessProfile.spec.login` (harness `claude-code`) | The Claude Code login file, for example a Claude subscription. |

## Sharing one Login

A Login is referenced by name, so many profiles can use the same Login:

- Many `HarnessProfile` objects can name one `model-api-key` or
  `claude-credentials` Login.
- Many `AgentProfile` objects can name one `github-token` Login, as long as
  each profile repository is in the Login's `repos`.

Runs that use the same Login can run at the same time. The launcher copies the
value into each sandbox when the run starts. A rotation applies to the runs
that start after it. Runs that are already running keep the old value.

## Create or rotate a Login with the API or the Logins page

The Logins page at `/logins` needs an owner token. It lists the Logins and
lets you add, rotate and delete them. A value is a text field or a file upload.

The same with the REST API. `PUT` creates the Login, or rotates it when it
exists:

```sh
curl -X PUT -H "Authorization: Bearer $OWNER_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "$(jq -n --arg v "$GITHUB_TOKEN" \
    '{kind: "github-token", repos: ["example/repo"], value: $v}')" \
  http://localhost:8080/v1/logins/github-example
```

Use `valueBase64` instead of `value` for a binary value or a file:

```sh
jq -n --arg v "$(base64 -w0 ~/.claude/.credentials.json)" \
  '{kind: "claude-credentials", description: "Claude subscription", valueBase64: $v}' |
curl -X PUT -H "Authorization: Bearer $OWNER_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @- \
  http://localhost:8080/v1/logins/claude-subscription
```

- A new Login and a kind change need a value.
- To change only the metadata, send the same kind without a value.
- Each change with a value sets `rotatedAt` and `rotatedBy`.

## Create or rotate a Login with kubectl

You can also write both parts directly. This is useful to copy a credential
that is already in the cluster.

1. Create the Secret in the logins namespace, with the key of the kind:

   ```sh
   kubectl -n agent-logins create secret generic claude-subscription \
     --from-file=credentials.json=$HOME/.claude/.credentials.json
   ```

   To copy an existing Secret without showing its value:

   ```sh
   kubectl -n source-ns get secret my-token -o json |
     jq '{apiVersion: "v1", kind: "Secret",
          metadata: {name: "github-example", namespace: "agent-logins"},
          data: {token: .data.token}}' |
     kubectl apply -f -
   ```

2. Create the `Login` object in the agents namespace:

   ```yaml
   apiVersion: agents.mudler.dev/v1alpha1
   kind: Login
   metadata:
     name: claude-subscription
     namespace: agents
   spec:
     kind: claude-credentials
     description: Claude subscription
   ```

To rotate, replace only the Secret value:

```sh
kubectl -n agent-logins create secret generic claude-subscription \
  --from-file=credentials.json=$HOME/.claude/.credentials.json \
  --dry-run=client -o yaml | kubectl apply -f -
```

With kubectl, `rotatedAt` and `rotatedBy` stay unchanged.

## What resets approval

An `AgentProfile` is approved against the UID of its GitHub Login. A
`HarnessProfile` is approved against its Login name.

- A rotation, with the API or with kubectl, keeps the approval.
- Deleting and re-creating the `Login` object gives it a new UID. The owner
  must approve the profiles that use it again.
- Pointing a `HarnessProfile` at another Login resets its approval.

## Claude subscription

Claude Code stores its login in `~/.claude/.credentials.json` on Linux. On
macOS it is in the Keychain. Log in with `claude` on a machine you trust, then
upload the file as a `claude-credentials` Login.

The file holds OAuth tokens that expire. A sandbox does not write a refreshed
token back to the Login. When runs fail to log in, log in again on your
machine and rotate the Login with the new file.
