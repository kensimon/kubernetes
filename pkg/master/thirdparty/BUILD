package(default_visibility = ["//visibility:public"])

licenses(["notice"])

load(
    "@io_bazel_rules_go//go:def.bzl",
    "go_library",
)

go_library(
    name = "go_default_library",
    srcs = ["thirdparty.go"],
    tags = ["automanaged"],
    deps = [
        "//pkg/api:go_default_library",
        "//pkg/apis/extensions:go_default_library",
        "//pkg/genericapiserver/endpoints:go_default_library",
        "//pkg/genericapiserver/endpoints/handlers:go_default_library",
        "//pkg/genericapiserver/registry/generic:go_default_library",
        "//pkg/genericapiserver/registry/rest:go_default_library",
        "//pkg/genericapiserver/server:go_default_library",
        "//pkg/registry/extensions/rest:go_default_library",
        "//pkg/registry/extensions/thirdpartyresourcedata:go_default_library",
        "//pkg/registry/extensions/thirdpartyresourcedata/storage:go_default_library",
        "//vendor:github.com/golang/glog",
        "//vendor:k8s.io/apimachinery/pkg/api/meta",
        "//vendor:k8s.io/apimachinery/pkg/apis/meta/v1",
        "//vendor:k8s.io/apimachinery/pkg/runtime",
        "//vendor:k8s.io/apimachinery/pkg/runtime/schema",
        "//vendor:k8s.io/apiserver/pkg/endpoints/request",
        "//vendor:k8s.io/apiserver/pkg/storage/storagebackend",
    ],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [":package-srcs"],
    tags = ["automanaged"],
)
