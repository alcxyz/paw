{
  image,
  jq,
  python3,
  reportScript,
  runtimeContents,
  skopeo,
  stdenvNoCC,
}:

stdenvNoCC.mkDerivation {
  pname = "paw-core-image-report";
  version = "0.0.0-dev";

  __structuredAttrs = true;
  exportReferencesGraph.runtimeClosure = runtimeContents;

  nativeBuildInputs = [
    jq
    python3
    skopeo
  ];

  buildCommand = ''
    mkdir -p "$TMPDIR/oci" "$out"
    skopeo --insecure-policy --tmpdir "$TMPDIR" copy --quiet \
      --dest-compress-format gzip \
      --dest-force-compress-format \
      docker-archive:${image} \
      oci:"$TMPDIR/oci":dev

    python3 ${reportScript} \
      --archive ${image} \
      --oci "$TMPDIR/oci" \
      --structured-attrs "$NIX_ATTRS_JSON_FILE" \
      --name paw-core \
      --tag dev \
      --output "$out/report.json"

    jq --exit-status . "$out/report.json" >/dev/null
  '';
}
