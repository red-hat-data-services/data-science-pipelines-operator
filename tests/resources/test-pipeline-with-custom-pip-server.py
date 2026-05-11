from kfp import dsl, compiler

# Keep this pipeline pinned to a custom pip server and explicit trusted host so
# integration tests are stable in CI environments with self-signed certificates.
@dsl.component(base_image="quay.io/opendatahub/ds-pipelines-ci-executor-image:v1.0",
packages_to_install=['numpy'],
pip_index_urls=['https://nginx-service.test-pypiserver.svc.cluster.local/simple/'],
pip_trusted_hosts=['nginx-service.test-pypiserver.svc.cluster.local'])
def say_hello() -> str:
    import numpy as np
    hello_text = f'Numpy version: {np.__version__}'
    print(hello_text)
    return hello_text


@dsl.pipeline
def hello_pipeline() -> str:
    hello_task = say_hello()
    return hello_task.output


if __name__ == '__main__':
    compiler.Compiler().compile(hello_pipeline, __file__.replace('.py', '-run.yaml'))
