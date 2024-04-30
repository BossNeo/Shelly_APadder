# MikroTik Shelly Device Fast Network Integration Script

## Overview
Introducing a dynamic MikroTik script designed to seamlessly integrate numerous Shelly devices into a selected network. This innovative solution empowers users with the ability to effortlessly add multiple Shelly devices, offering customizable features such as automatic AP shutdown upon successful integration, cloud activation, and the flexibility to assign static IP addresses in ascending order. Elevate your network management experience with unparalleled efficiency and control.

## Features
- **Fast Integration**: Quickly adds multiple Shelly devices to the network.
- **Enhanced Manageability**: Simplifies device manipulation through network management tools.
- **Flexible Configuration**: Customizable settings within the script for seamless integration.

## Requirements
- MikroTik router running RouterOS
- Router configured as a station with a clear security profile

## Installation
1. Download the script file (`The_script.rsc`) from the [GitHub repository](link-to-repo).
2. Connect to your MikroTik router via WinBox or SSH.
3. Open the Terminal window and upload the script file to the router's filesystem.

   ```bash
   /file upload file-name=The_script.rsc
