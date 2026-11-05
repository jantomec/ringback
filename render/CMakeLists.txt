cmake_minimum_required(VERSION 3.28)
project(ringback_render LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)

# OpenUSD: on a Mac from the pixi environment (pixi.toml), which build.sh puts on
# CMAKE_PREFIX_PATH; on Linux the system's. Its targets name OpenGL's.
find_package(OpenGL REQUIRED)
find_package(pxr REQUIRED CONFIG)

add_executable(ringback-render src/main.cpp src/gpu.cpp src/scene.cpp src/renderer.cpp src/still.cpp)
target_link_libraries(ringback-render PRIVATE usd usdGeom usdImagingGL hd hdx hgi hio glf cameraUtil)
if(NOT APPLE)
    # Storm's OpenGL context, without a display (gpu.cpp).
    find_library(EGL_LIBRARY EGL REQUIRED)
    target_link_libraries(ringback-render PRIVATE ${EGL_LIBRARY})
endif()
# It finds OpenUSD's libraries where it was built against them.
set_target_properties(ringback-render PROPERTIES BUILD_RPATH "${pxr_DIR}/lib")
