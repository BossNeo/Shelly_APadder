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
1. Download the script file (`TheScript.rsc`) from the [GitHub repository](link-to-repo).
2. Connect to your MikroTik router via WinBox or SSH.
3. Open the Terminal window and upload the script file to the router's filesystem.

   ```bash
   /file upload file-name=TheScript.rsc
## Configuration
1. Open the script file (ThScript.rsc) in a text editor.
2. Modify the configurable settings within the script.
3. Network selection for Shelly devices.
   
   ```bash
  :global SSIDName Shelly%203;
   ```bash
  :global PASS 12345678;
  
Automatic AP shutdown after device connection.
:global TurnOffAP 1;  // 1 is on 0 is off
Manual IP address assignment starting point.

## Usage
Ensure your MikroTik router is configured as a station with a clear security profile.
Run the script by executing the import command as mentioned in the installation steps.

##  Contributing
Contributions are welcome! Please fork the repository and submit a pull request with any improvements or features you'd like to add.

License
This project is licensed under the GPL v3 License.

Acknowledgments
Thanks to the MikroTik community for inspiration and support.
