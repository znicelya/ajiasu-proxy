fn main() -> Result<(), Box<dyn std::error::Error>> {
    let protoc = protoc_bin_vendored::protoc_bin_path()?;
    let gateway = "../../api/proto/gateway/v1/gateway.proto";
    let relay = "../../api/proto/relay/v1/relay.proto";
    println!("cargo:rerun-if-changed={gateway}");
    println!("cargo:rerun-if-changed={relay}");
    let mut config = prost_build::Config::new();
    config.protoc_executable(protoc);
    config.type_attribute(".", "#[allow(clippy::large_enum_variant)]");
    tonic_prost_build::configure().compile_with_config(
        config,
        &[gateway, relay],
        &["../../api/proto"],
    )?;
    Ok(())
}
