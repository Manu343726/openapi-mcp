# OpenAPI-MCP development container.
#
# Based on the official Microsoft Go devcontainer image, extended with the
# opencode CLI (an open-source AI coding assistant) so agents can drive the
# MCP server that this repo implements.
#
# opencode docs: https://opencode.ai
# opencode installer: https://opencode.ai/install (installs a standalone binary,
# so no Node.js/npm is required in the image).

# The Microsoft Go devcontainer images are published as
# mcr.microsoft.com/devcontainers/go:<major> or :<major>-<minor>.
ARG VARIANT="1"
FROM mcr.microsoft.com/devcontainers/go:${VARIANT}

# opencode is a standalone CLI; /usr/local/bin is on the default PATH.
RUN curl -fsSL https://opencode.ai/install | bash \
    && mv ~/.opencode/bin/opencode /usr/local/bin/opencode \
    && opencode --version

# --- Container runtime conveniences for the code in this repo ---

# The MCP server reads a JSON config file that is written back to on runtime
# registrations. Default location inside the container.
ENV OPENAPI_MCP_CONFIG=/workspace/.config/config.yaml
ENV OPENAPI_MCP_PORT=8080

# The MCP server connects via SSE over HTTP. Expose it for host-side MCP
# clients (VS Code, opencode running on the host) to reach localhost.
EXPOSE 8080
EXPOSE 8099
