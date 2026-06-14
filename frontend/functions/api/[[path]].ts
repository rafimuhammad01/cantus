// Proxies every /api/* request from Cloudflare Pages to the EC2 backend.
//
// Browser → https://<cantus>.pages.dev/api/... → this Function → http://<EC2>/api/...
//
// Set CANTUS_BACKEND_URL in Pages env vars, e.g. "http://54.169.205.65".

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
  const target = new URL(
    incoming.pathname + incoming.search,
    env.CANTUS_BACKEND_URL,
  );

  try {
    const upstream = await fetch(target.toString(), {
      method: request.method,
      headers: request.headers,
      body:
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : request.body,
      redirect: "manual",
    });
    return new Response(upstream.body, {
      status: upstream.status,
      statusText: upstream.statusText,
      headers: upstream.headers,
    });
  } catch (err) {
    return new Response(
      JSON.stringify({
        error: "upstream fetch failed",
        target: target.toString(),
        detail: String(err),
      }),
      { status: 502, headers: { "content-type": "application/json" } },
    );
  }
};
