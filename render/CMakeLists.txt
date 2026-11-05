cmake_minimum_required(VERSION 3.28)
project(ringback_render LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)

# OpenUSD: on a Mac from the pixi environment (pixi.toml), which build.sh puts on
# CMAKE_PREFIX_PATH; on Linux the system's. Its targets name OpenGL's.
find_package(OpenGL REQUIRED)
if(NOT APPLE)
    # A distribution's OpenSubdiv can be built with CUDA (Arch's is), and its targets then name
    # CUDA's, which a system without the CUDA toolkit lacks. ringback-render doesn't use them.
    find_package(CUDAToolkit QUIET)
    if(NOT TARGET CUDA::cudart_static)
        add_library(CUDA::cudart_static INTERFACE IMPORTED)
    endif()
endif()
find_package(pxr REQUIRED CONFIG)

add_executable(ringback-render src/main.cpp src/gpu.cpp src/scene.cpp src/renderer.cpp src/still.cpp)
target_link_libraries(ringback-render PRIVATE usd usdGeom usdImagingGL hd hdx hgi hio glf cameraUtil)
if(TARGET usd_m)
    # A monolithic OpenUSD, as Arch's: one library, whose modules' targets are empty. By its file:
    # its target names libraries of its plugins' that aren't there to link, and it finds its own.
    target_link_libraries(ringback-render PRIVATE "$<TARGET_FILE:usd_m>")
endif()
if(CMAKE_CXX_COMPILER_ID STREQUAL "GNU")
    # OpenUSD's own headers include ones GCC calls deprecated.
    target_compile_options(ringback-render PRIVATE -Wno-deprecated)
endif()
if(NOT APPLE)
    # Storm's OpenGL context, without a display (gpu.cpp).
    find_library(EGL_LIBRARY EGL REQUIRED)
    target_link_libraries(ringback-render PRIVATE ${EGL_LIBRARY})
endif()
# It finds OpenUSD's libraries where it was built against them.
set_target_properties(ringback-render PROPERTIES BUILD_RPATH "${pxr_DIR}/lib")
