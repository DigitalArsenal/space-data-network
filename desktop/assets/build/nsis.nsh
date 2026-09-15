ManifestDPIAware true

!macro customInit
    # Installing for all users needs elevation; ask for it before the installer
    # starts writing anywhere it cannot reach.
    ${if} $installMode == "all"
        ${IfNot} ${UAC_IsAdmin}
            ShowWindow $HWNDPARENT ${SW_HIDE}
            !insertmacro UAC_RunElevated
            Quit
        ${endif}
    ${endif}
!macroend
