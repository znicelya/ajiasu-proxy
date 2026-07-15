fn main() -> Result<(), Box<dyn std::error::Error>> {
    let protoc = protoc_bin_vendored::protoc_bin_path()?;
    let proto = "../../api/proto/agent/v1/agent.proto";
    println!("cargo:rerun-if-changed={proto}");
    let mut config = prost_build::Config::new();
    config.protoc_executable(protoc);
    config.type_attribute(".", "#[allow(clippy::large_enum_variant)]");
    tonic_prost_build::configure().compile_with_config(config, &[proto], &["../../api/proto"])?;
    Ok(())
}
