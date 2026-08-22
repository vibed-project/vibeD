---
sidebar_position: 16
---

# User & department admin

Four MCP tools expose vibeD's user and department registry so an admin agent can
inspect who can deploy and how they are grouped. These are read/list tools plus
one department creator; vibeD does not create or delete users through MCP.

:::note Requires a user store backend
These tools are only registered when the configured [`store` backend](../configuration/config-reference.md) implements user persistence. The bundled `sqlite` backend does; the `configmap` (and in-memory) backends do **not**. With a non-user store the four tools are simply absent from the MCP tool list. An out-of-tree store that implements the same interface enables them too.
:::

Access is role-scoped. `list_users`, `list_departments`, and `create_department`
require the caller to hold the `admin` role. `get_user` lets a regular user read
their own record and an admin read anyone's.

## Tools

| Tool | Role | Description |
|------|------|-------------|
| [`list_users`](#list_users) | admin | List all users, optionally filtered by department |
| [`get_user`](#get_user) | self / admin | Get one user's details |
| [`list_departments`](#list_departments) | admin | List all departments |
| [`create_department`](#create_department) | admin | Create a department |

---

## list_users

List all vibeD users. Requires the `admin` role. Optionally filter by
department.

### Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `department_id` | string | No | Filter users by department ID. Omit to list all users. |

### Example

```json
{
  "department_id": "dept-17a3f9c2e1"
}
```

### Response

```json
{
  "users": [
    {
      "id": "u-9f2a",
      "name": "alice",
      "email": "alice@example.com",
      "role": "admin",
      "status": "active",
      "provider": "oidc",
      "department_id": "dept-17a3f9c2e1",
      "created_at": "2026-03-14T10:00:00Z",
      "updated_at": "2026-03-14T10:00:00Z"
    }
  ]
}
```

A non-admin caller receives an `admin access required` error. The API-key hash
is never included in the response.

---

## get_user

Get details of a specific user. Admins can view any user; a regular user can
only view themselves.

### Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `user_id` | string | Yes | ID of the user to retrieve |

### Example

```json
{
  "user_id": "u-9f2a"
}
```

### Response

```json
{
  "id": "u-9f2a",
  "name": "alice",
  "email": "alice@example.com",
  "role": "developer",
  "status": "active",
  "provider": "local",
  "department_id": "dept-17a3f9c2e1",
  "created_at": "2026-03-14T10:00:00Z",
  "updated_at": "2026-03-14T10:00:00Z"
}
```

When a non-admin caller requests any `user_id` other than their own, the tool
returns `user not found` rather than distinguishing "forbidden" from "does not
exist".

---

## list_departments

List all departments. Requires the `admin` role.

### Input Schema

Takes no parameters.

```json
{}
```

### Response

```json
{
  "departments": [
    {
      "id": "dept-17a3f9c2e1",
      "name": "Platform",
      "namespace": "vibed-apps",
      "created_at": "2026-03-14T10:00:00Z",
      "updated_at": "2026-03-14T10:00:00Z"
    }
  ]
}
```

An empty registry returns `{"departments": []}`. A non-admin caller receives an
`admin access required` error.

---

## create_department

Create a new department. Requires the `admin` role.

### Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Name of the department to create |

### Example

```json
{
  "name": "Platform"
}
```

### Response

```json
{
  "id": "dept-17a3f9c2e1",
  "name": "Platform",
  "namespace": "",
  "created_at": "2026-03-14T10:00:00Z",
  "updated_at": "2026-03-14T10:00:00Z"
}
```

The `id` is server-generated (`dept-<hex>`). An empty `name` returns a
`name is required` error, and a non-admin caller returns `admin access required`.
