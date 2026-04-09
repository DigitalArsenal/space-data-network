include_guard(GLOBAL)

include(FetchContent)

# Internal OrbPro Crypto++ wrapper.
#
# This avoids depending on an external CMake wrapper repository while still
# allowing reproducible pinning and local-source overrides.
#
# Public cache variables:
#   ORBPRO_CRYPTOPP_SOURCE_DIR      Local checkout path to Crypto++ sources.
#   ORBPRO_CRYPTOPP_GIT_REPOSITORY  Remote repo when local source is not set.
#   ORBPRO_CRYPTOPP_GIT_TAG         Pinned revision/tag for remote fetch.

set(
  ORBPRO_CRYPTOPP_GIT_REPOSITORY
  "https://github.com/weidai11/cryptopp.git"
  CACHE STRING
  "Crypto++ Git repository used by OrbPro wrapper."
)
set(
  ORBPRO_CRYPTOPP_GIT_TAG
  "CRYPTOPP_8_9_0"
  CACHE STRING
  "Crypto++ Git revision/tag used by OrbPro wrapper."
)
set(
  ORBPRO_CRYPTOPP_SOURCE_DIR
  ""
  CACHE PATH
  "Optional local Crypto++ source directory (offline builds)."
)

function(orbpro_use_cryptopp)
  if(TARGET cryptopp)
    return()
  endif()

  set(_orbpro_cryptopp_effective_source_dir "")

  if(ORBPRO_CRYPTOPP_SOURCE_DIR)
    if(EXISTS "${ORBPRO_CRYPTOPP_SOURCE_DIR}/aes.h")
      set(_orbpro_cryptopp_effective_source_dir "${ORBPRO_CRYPTOPP_SOURCE_DIR}")
    elseif(EXISTS "${ORBPRO_CRYPTOPP_SOURCE_DIR}/cryptopp/aes.h")
      set(_orbpro_cryptopp_effective_source_dir "${ORBPRO_CRYPTOPP_SOURCE_DIR}/cryptopp")
    else()
      message(
        FATAL_ERROR
        "ORBPRO_CRYPTOPP_SOURCE_DIR does not look like a Crypto++ source tree: ${ORBPRO_CRYPTOPP_SOURCE_DIR}"
      )
    endif()

    message(
      STATUS
      "OrbPro Crypto++ wrapper using local source: ${_orbpro_cryptopp_effective_source_dir}"
    )
    FetchContent_Declare(
      orbpro_cryptopp
      SOURCE_DIR "${_orbpro_cryptopp_effective_source_dir}"
    )
  else()
    message(
      STATUS
      "OrbPro Crypto++ wrapper fetching from ${ORBPRO_CRYPTOPP_GIT_REPOSITORY} @ ${ORBPRO_CRYPTOPP_GIT_TAG}"
    )
    FetchContent_Declare(
      orbpro_cryptopp
      GIT_REPOSITORY "${ORBPRO_CRYPTOPP_GIT_REPOSITORY}"
      GIT_TAG "${ORBPRO_CRYPTOPP_GIT_TAG}"
      GIT_SHALLOW TRUE
    )
  endif()

  FetchContent_MakeAvailable(orbpro_cryptopp)

  if(NOT EXISTS "${orbpro_cryptopp_SOURCE_DIR}/aes.h")
    message(
      FATAL_ERROR
      "Failed to locate Crypto++ headers under ${orbpro_cryptopp_SOURCE_DIR}"
    )
  endif()

  file(GLOB _orbpro_cryptopp_sources CONFIGURE_DEPENDS
    "${orbpro_cryptopp_SOURCE_DIR}/*.cpp"
  )

  foreach(_pattern
      "adhoc.cpp"
      "bench*.cpp"
      "cryptest*.cpp"
      "datatest.cpp"
      "dlltest.cpp"
      "fipsalgt.cpp"
      "fipstest.cpp"
      "regtest*.cpp"
      "test*.cpp"
      "validat*.cpp")
    file(GLOB _matches "${orbpro_cryptopp_SOURCE_DIR}/${_pattern}")
    if(_matches)
      list(REMOVE_ITEM _orbpro_cryptopp_sources ${_matches})
    endif()
  endforeach()

  if(NOT _orbpro_cryptopp_sources)
    message(
      FATAL_ERROR
      "OrbPro Crypto++ wrapper found no compilable Crypto++ source files."
    )
  endif()

  add_library(cryptopp STATIC ${_orbpro_cryptopp_sources})
  add_library(orbpro::cryptopp ALIAS cryptopp)

  # Crypto++ source trees expose headers at repo root (e.g. aes.h), while
  # consumers often include using <cryptopp/aes.h>. Create a stable include
  # root containing a "cryptopp" alias so both styles work.
  set(_orbpro_cryptopp_include_root "${CMAKE_CURRENT_BINARY_DIR}/orbpro-cryptopp-include")
  set(_orbpro_cryptopp_include_alias "${_orbpro_cryptopp_include_root}/cryptopp")
  file(MAKE_DIRECTORY "${_orbpro_cryptopp_include_root}")
  if(NOT EXISTS "${_orbpro_cryptopp_include_alias}")
    file(
      CREATE_LINK
      "${orbpro_cryptopp_SOURCE_DIR}"
      "${_orbpro_cryptopp_include_alias}"
      SYMBOLIC
      COPY_ON_ERROR
    )
  endif()

  target_include_directories(
    cryptopp
    PUBLIC
      "${orbpro_cryptopp_SOURCE_DIR}"
      "${_orbpro_cryptopp_include_root}"
  )
  target_compile_features(cryptopp PUBLIC cxx_std_17)

  if(EMSCRIPTEN)
    target_compile_definitions(
      cryptopp
      PUBLIC
        CRYPTOPP_DISABLE_ASM=1
        CRYPTOPP_DISABLE_SSSE3=1
        CRYPTOPP_DISABLE_AESNI=1
    )
  endif()
endfunction()
