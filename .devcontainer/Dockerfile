# OpenAPI-MCP development container.
#
# Based on the official Microsoft Go devcontainer image, extended with the
# opencode CLI (an open-source AI coding assistant) so agents can drive the
# MCP server that this repo implements.
#
# opencode docs: https://opencode.ai
# opencode installer: https://opencode.ai/install (installs a standalone binary;
# Node.js/npm are added below only for the mcp-debugger MCP server).

# The Microsoft Go devcontainer images are published as
# mcr.microsoft.com/devcontainers/go:<major> or :<major>-<minor>.
ARG VARIANT="1"
FROM mcr.microsoft.com/devcontainers/go:${VARIANT}

# opencode is a standalone CLI; /usr/local/bin is on the default PATH.
RUN curl -fsSL https://opencode.ai/install | bash \
    && mv ~/.opencode/bin/opencode /usr/local/bin/opencode \
    && opencode --version

# mcp-debugger (https://github.com/debugmcp/mcp-debugger) is a step-through
# debugging MCP server for LLMs/opencode. It requires Node.js 22+, which the
# base Go image does not ship (it only provides nvm), so install Node 22 via
# nvm, expose node/npm/npx on PATH, and install the debugger globally under
# /usr/local/bin so opencode can launch it as a local MCP server. The version
# check fails the build if the install did not succeed.
RUN set -eux; \
    . /usr/local/share/nvm/nvm.sh; \
    nvm install 22; \
    nvm alias default 22 >/dev/null; \
    ln -s "$(command -v node)" /usr/local/bin/node; \
    ln -s "$(command -v npm)" /usr/local/bin/npm; \
    ln -s "$(command -v npx)" /usr/local/bin/npx; \
    node --version; \
    npm install -g --prefix /usr/local @debugmcp/mcp-debugger; \
    mcp-debugger --version

# --- Container runtime conveniences for the code in this repo ---

# The MCP server reads a JSON config file that is written back to on runtime
# registrations. Default location inside the container.
ENV OPENAPI_MCP_CONFIG=/workspace/.config/config.yaml
ENV OPENAPI_MCP_PORT=8080

# The MCP server connects via SSE over HTTP. Expose it for host-side MCP
# clients (VS Code, opencode running on the host) to reach localhost.
EXPOSE 8080
EXPOSE 8099
