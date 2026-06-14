// Proxies /api/* from Cloudflare Pages to the EC2 backend.

export const onRequest: PagesFunction<{ CANTUS_BACKEND_URL: string }> = async (
  context,
) => {
  const { request, env } = context;

  if (!env.CANTUS_BACKEND_URL) {
    return new Response(
      JSON.stringify({ error: "CANTUS_BACKEND_URL not set" }),
      { status: 500, headers: { "content-type": "application/json" } },
    );
  }

  const incoming = new URL(request.url);
  const target =
    env.CANTUS_BACKEND_URL.replace(/\/$/, "") +
    incoming.pathname +
    incoming.search;

  let upstreamStatus = 0;
  let upstreamBody = "";
  let upstreamContentType = "application/octet-stream";

  try {
    // Strip browser-supplied headers; only carry through method + body.
    const forwardHeaders = new Headers();
    const ct = request.headers.get("content-type");
    if (ct) forwardHeaders.set("content-type", ct);
    const accept = request.headers.get("accept");
    if (accept) forwardHeaders.set("accept", accept);

    const init: RequestInit = {
      method: request.method,
      headers: forwardHeaders,
      redirect: "manual",
    };
    if (request.method !== "GET" && request.method !== "HEAD") {
      init.body = await request.text();
    }

    const upstream = await fetch(target, init);
    upstreamStatus = upstream.status;
    upstreamContentType =
      upstream.headers.get("content-type") ?? "application/octet-stream";
    upstreamBody = await upstream.text();
  } catch (err) {
    return new Response(
      JSON.stringify({
        error: "upstream fetch failed",
        target,
        detail: String(err),
        stack: err instanceof Error ? err.stack : undefined,
      }),
      { status: 502, headers: { "content-type": "application/json" } },
    );
  }

  return new Response(upstreamBody, {
    status: upstreamStatus,
    headers: { "content-type": upstreamContentType },
  });
};
