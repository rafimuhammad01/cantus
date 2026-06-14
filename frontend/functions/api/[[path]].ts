// Probe: try two outbound fetches and return the result of each.
// Tells us whether Workers can fetch at all (httpbin), and whether the EC2
// HTTP origin is the specific problem.

export const onRequest: PagesFunction<{
  CANTUS_BACKEND_URL: string;
}> = async () => {
  const out: Record<string, unknown> = {};

  // Probe 1: known-good HTTPS public URL
  try {
    const r = await fetch("https://httpbin.org/get");
    out.httpbin_status = r.status;
    out.httpbin_body_len = (await r.text()).length;
  } catch (err) {
    out.httpbin_error = String(err);
  }

  // Probe 2: EC2 plain HTTP
  try {
    const r = await fetch("http://54.169.205.65/health");
    out.ec2_status = r.status;
    out.ec2_body = await r.text();
  } catch (err) {
    out.ec2_error = String(err);
  }

  return new Response(JSON.stringify(out, null, 2), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
};
